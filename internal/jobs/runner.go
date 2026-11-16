package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Runner 从队列取任务并分派给 Handler。
//
// 设计取舍：
//   - 并发度默认 1。SQLite 是单写者，队列任务又几乎都写库；
//     开多并发只会制造锁等待，而不是吞吐。
//   - 空闲时用可取消的定时器等待，不用轮询循环空转。
//   - 每个任务独立 ctx：取消某个任务不影响其他任务。
type Runner struct {
	store    *Store
	handlers map[string]Handler
	log      *slog.Logger

	workers  int
	idleWait time.Duration
	// taskTimeout 是单个任务的执行上限。本地 CPU 嵌入 200 页可能很久，
	// 默认给 30 分钟。
	taskTimeout time.Duration

	wake chan struct{}

	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopping atomic.Bool
	stopOnce sync.Once
}

// RunnerOptions 配置执行器。
type RunnerOptions struct {
	Workers     int           // 默认 1
	IdleWait    time.Duration // 空队列时的轮询间隔，默认 2s
	TaskTimeout time.Duration // 单任务超时，默认 30min
	Logger      *slog.Logger
}

// NewRunner 构造执行器。
func NewRunner(store *Store, opts RunnerOptions) *Runner {
	if opts.Workers <= 0 {
		opts.Workers = 1
	}
	if opts.IdleWait <= 0 {
		opts.IdleWait = 2 * time.Second
	}
	if opts.TaskTimeout <= 0 {
		opts.TaskTimeout = 30 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Runner{
		store:       store,
		handlers:    make(map[string]Handler),
		log:         opts.Logger,
		workers:     opts.Workers,
		idleWait:    opts.IdleWait,
		taskTimeout: opts.TaskTimeout,
		wake:        make(chan struct{}, 1),
	}
}

// Register 注册 handler。同一 kind 重复注册会覆盖。
func (r *Runner) Register(h Handler) {
	r.handlers[h.Kind()] = h
}

// Start 启动 worker。ctx 取消后优雅停止：不再取新任务，
// 正在跑的任务跑完。
func (r *Runner) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.loop(ctx, i)
	}
}

// Stop 优雅停止：标记不再取新任务，等正在跑的任务自然结束。
//
// 不取消 ctx。取消会让正在执行的任务半途收到 context canceled——用户
// 刚 /kb add 的文档就再也补不上向量，任务状态还是 failed，看起来像
// 配置错了。真正需要立刻中断时用 Cancel。
//
// 等待没有上限：一个嵌入任务最多受 taskTimeout 约束。调用方若不能等，
// 用 Cancel 强停。
func (r *Runner) Stop() {
	r.stopOnce.Do(func() { r.stopping.Store(true) })
	r.wg.Wait()
}

// Cancel 立即中断所有 worker 与正在执行的任务。用于进程退出且不愿等待。
func (r *Runner) Cancel() {
	r.stopOnce.Do(func() { r.stopping.Store(true) })
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

// Notify 唤醒空闲的 worker，让新入队的任务立即执行而不是等轮询。
func (r *Runner) Notify() {
	select {
	case r.wake <- struct{}{}:
	default: // 已有待处理的唤醒，无需重复
	}
}

func (r *Runner) loop(ctx context.Context, id int) {
	defer r.wg.Done()
	for {
		if r.stopping.Load() {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := r.store.Claim(ctx, time.Now())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				r.idle(ctx)
				continue
			}
			if ctx.Err() != nil || r.stopping.Load() {
				return
			}
			r.log.Error("领取任务失败", "worker", id, "error", err)
			r.idle(ctx)
			continue
		}

		// 领到任务后即使正在停止也要跑完，否则它会卡在 running 状态，
		// 下次启动要靠 RequeueStale 才能恢复。
		r.run(ctx, job)
	}
}

func (r *Runner) idle(ctx context.Context) {
	t := time.NewTimer(r.idleWait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-r.wake:
	case <-t.C:
	}
}

func (r *Runner) run(parent context.Context, job Job) {
	h, ok := r.handlers[job.Kind]
	if !ok {
		// 没有 handler 说明版本不一致或注册漏了。留着任务会无限重试，
		// 直接判失败并把原因写清楚。
		err := errors.New("没有注册处理该任务的 handler")
		r.log.Error("未知任务类型", "kind", job.Kind, "job", job.ID)
		r.fail(parent, job, err, true)
		return
	}

	ctx, cancel := context.WithTimeout(parent, r.taskTimeout)
	defer cancel()

	r.log.Info("开始任务", "job", job.ID, "kind", job.Kind, "attempt", job.Attempts)
	start := time.Now()
	err := safeHandle(ctx, h, job)
	if err == nil {
		r.log.Info("任务完成", "job", job.ID, "kind", job.Kind, "耗时", time.Since(start))
		if e := r.store.Succeed(parent, job.ID); e != nil {
			r.log.Error("标记任务完成失败", "job", job.ID, "error", e)
		}
		return
	}

	canceled := errors.Is(err, ErrCancel)
	if ctx.Err() == context.DeadlineExceeded {
		err = errors.New("任务执行超时")
	}
	r.log.Warn("任务失败", "job", job.ID, "kind", job.Kind, "attempt", job.Attempts,
		"cancel", canceled, "error", err)
	r.fail(parent, job, err, canceled)
}

// safeHandle 隔离 handler panic：一个坏 handler 不该带走整个进程。
func safeHandle(ctx context.Context, h Handler, job Job) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = errors.New("handler panic")
		}
	}()
	return h.Handle(ctx, job)
}

func (r *Runner) fail(ctx context.Context, job Job, cause error, canceled bool) {
	// 用可取消的父 ctx 会因 Stop 而失败，所以这里用独立 ctx
	if err := r.store.Fail(context.WithoutCancel(ctx), job.ID, cause, canceled); err != nil {
		r.log.Error("记录任务失败状态出错", "job", job.ID, "error", err)
	}
}

// RunOnce 同步执行一条任务，供测试与「立即处理」的场景使用。
func (r *Runner) RunOnce(ctx context.Context, job Job) error {
	h, ok := r.handlers[job.Kind]
	if !ok {
		return errors.New("没有注册处理该任务的 handler")
	}
	ctx, cancel := context.WithTimeout(ctx, r.taskTimeout)
	defer cancel()
	return h.Handle(ctx, job)
}

// HasHandler 报告是否注册了该类型。
func (r *Runner) HasHandler(kind string) bool {
	_, ok := r.handlers[kind]
	return ok
}
