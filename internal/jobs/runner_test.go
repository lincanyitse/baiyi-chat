package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type countingHandler struct {
	handled atomic.Int32
	fail    error
	delay   time.Duration
}

func (h *countingHandler) Kind() string { return "count" }

func (h *countingHandler) Handle(ctx context.Context, job Job) error {
	h.handled.Add(1)
	if h.delay > 0 {
		select {
		case <-time.After(h.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.fail
}

func quietRunner(store *Store) *Runner {
	return NewRunner(store, RunnerOptions{
		IdleWait: 10 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestRunnerProcessesJob(t *testing.T) {
	store := newTestStore(t)
	h := &countingHandler{}
	r := quietRunner(store)
	r.Register(h)

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "count"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	defer r.Stop()
	r.Notify()

	waitFor(t, time.Second, func() bool { return h.handled.Load() == 1 })

	after, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusDone {
		t.Fatalf("任务应为 done，实际 %s", after.Status)
	}
}

func TestRunnerRetriesThenSucceeds(t *testing.T) {
	store := newTestStore(t)
	h := &countingHandler{fail: errors.New("临时失败")}
	r := quietRunner(store)
	r.Register(h)

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "count", MaxAttempts: 2})
	// 直接同步跑两次，避免等待退避
	for range 2 {
		claimed, err := store.Claim(context.Background(), time.Now())
		if err != nil {
			// 第一次失败后 run_after 在未来，手动拨到过去
			store.db.Exec(`UPDATE jobs SET run_after = 0`)
			claimed, err = store.Claim(context.Background(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
		if claimed.ID != job.ID {
			t.Fatalf("领取了错误的任务")
		}
		r.run(context.Background(), claimed)
	}

	after, _ := store.Get(context.Background(), job.ID)
	if after.Status != StatusFailed {
		t.Fatalf("耗尽重试后应为 failed，实际 %s", after.Status)
	}
	if after.Attempts != 2 {
		t.Fatalf("应尝试 2 次，实际 %d", after.Attempts)
	}
}

func TestRunnerUnknownKindFailsImmediately(t *testing.T) {
	store := newTestStore(t)
	r := quietRunner(store)

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "nobody"})
	claimed, _ := store.Claim(context.Background(), time.Now())
	r.run(context.Background(), claimed)

	after, _ := store.Get(context.Background(), job.ID)
	if after.Status != StatusCanceled {
		t.Fatalf("没有 handler 应直接 canceled，实际 %s", after.Status)
	}
}

func TestRunnerHandlerPanicIsolated(t *testing.T) {
	store := newTestStore(t)
	r := quietRunner(store)
	r.Register(panicHandler{})

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "panic", MaxAttempts: 1})
	claimed, _ := store.Claim(context.Background(), time.Now())
	r.run(context.Background(), claimed)

	after, _ := store.Get(context.Background(), job.ID)
	if after.Status != StatusFailed {
		t.Fatalf("panic 应记录为失败，实际 %s", after.Status)
	}
}

type panicHandler struct{}

func (panicHandler) Kind() string { return "panic" }
func (panicHandler) Handle(context.Context, Job) error {
	panic("boom")
}

func TestRunnerErrCancelSkipsRetry(t *testing.T) {
	store := newTestStore(t)
	r := quietRunner(store)
	r.Register(&countingHandler{fail: ErrCancel})

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "count", MaxAttempts: 5})
	claimed, _ := store.Claim(context.Background(), time.Now())
	r.run(context.Background(), claimed)

	after, _ := store.Get(context.Background(), job.ID)
	if after.Status != StatusCanceled {
		t.Fatalf("ErrCancel 应直接 canceled，实际 %s", after.Status)
	}
}

func TestRunnerTaskTimeout(t *testing.T) {
	store := newTestStore(t)
	h := &countingHandler{delay: time.Second}
	r := NewRunner(store, RunnerOptions{
		TaskTimeout: 50 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	r.Register(h)

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "count", MaxAttempts: 1})
	claimed, _ := store.Claim(context.Background(), time.Now())
	r.run(context.Background(), claimed)

	after, _ := store.Get(context.Background(), job.ID)
	if after.Status != StatusFailed {
		t.Fatalf("超时应记录失败，实际 %s", after.Status)
	}
	if after.LastError != "任务执行超时" {
		t.Fatalf("超时原因应可辨识，实际 %q", after.LastError)
	}
}

func TestRunnerHasHandler(t *testing.T) {
	r := quietRunner(newTestStore(t))
	r.Register(&countingHandler{})
	if !r.HasHandler("count") {
		t.Fatal("应报告已注册")
	}
	if r.HasHandler("nope") {
		t.Fatal("未注册的类型不该报告存在")
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// TestStopWaitsForRunningJob 锁定一个真实缺陷：
// 曾用「取消 ctx 再等待」实现 Stop，导致正在跑的嵌入任务半途收到
// context canceled——用户刚 /kb add 的文档永远补不上向量。
func TestStopWaitsForRunningJob(t *testing.T) {
	store := newTestStore(t)
	h := &countingHandler{delay: 120 * time.Millisecond}
	r := NewRunner(store, RunnerOptions{
		IdleWait: 5 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	r.Register(h)

	job, _, _ := store.Enqueue(context.Background(), Enqueue{Kind: "count"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Notify()

	waitFor(t, time.Second, func() bool { return h.handled.Load() == 1 })

	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 应在任务完成后返回")
	}

	after, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusDone {
		t.Fatalf("停止时正在跑的任务应跑完，实际 %s（%s）", after.Status, after.LastError)
	}
}
