package embed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"
)

// Limiter 给嵌入与重排调用加上「令牌桶限流 + 批切分 + 退避重试 + 用量记账」。
//
// 为什么需要它：嵌入是唯一会被批量触发的出网调用（200 页语料一次入队
// 就是几百次请求），没有节流会直接触发厂商 429；而调用方（ingest、
// 重建、检索）都不该各自实现一遍重试与计数。
//
// Limiter 包装 Embedder/Reranker 并原样实现这两个接口，因此可以透明替换。
type Limiter struct {
	inner   Embedder
	rerank  Reranker
	limiter *rate.Limiter
	batches int // 每批条数，<=0 表示不切批
	meter   Meter
	backend string

	maxRetries int
	backoff    time.Duration
	// retryBudget 是一批任务的重试总时长上限，防止慢服务拖住用户。
	retryBudget time.Duration
}

// LimiterOptions 配置限流器。
type LimiterOptions struct {
	// RPS 是每秒请求数上限，<=0 表示不限流。
	RPS float64
	// Burst 是允许的瞬时突发，<=0 时取 max(1, ceil(RPS))。
	Burst int
	// BatchSize 是单次 Embed 调用里的最大条数，<=0 时取 64。
	BatchSize int
	// MaxRetries 是遇到暂时性错误时的重试次数，默认 3。
	MaxRetries int
	// Backoff 是重试的初始退避，默认 1s，每次翻倍。
	Backoff time.Duration
	// RetryBudget 是一批任务全部重试的时长上限，默认 10s。
	// 超过即放弃并让调用方降级——检索是交互操作，等不起。
	RetryBudget time.Duration
	// Meter 接收用量。nil 时用 NoopMeter。
	Meter Meter
}

// NewLimiter 包装嵌入后端。rerank 可为 nil（表示该后端没有重排能力）。
func NewLimiter(inner Embedder, rerank Reranker, opts LimiterOptions) *Limiter {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.RetryBudget <= 0 {
		opts.RetryBudget = 10 * time.Second
	}
	if opts.Meter == nil {
		opts.Meter = NoopMeter{}
	}

	var rl *rate.Limiter
	if opts.RPS > 0 {
		burst := opts.Burst
		if burst <= 0 {
			burst = max(int(opts.RPS), 1)
		}
		rl = rate.NewLimiter(rate.Limit(opts.RPS), burst)
	}

	return &Limiter{
		inner:       inner,
		rerank:      rerank,
		limiter:     rl,
		batches:     opts.BatchSize,
		meter:       opts.Meter,
		backend:     inner.Name(),
		maxRetries:  opts.MaxRetries,
		backoff:     opts.Backoff,
		retryBudget: opts.RetryBudget,
	}
}

// Name 实现 Embedder。
func (l *Limiter) Name() string { return l.inner.Name() }

// Dimensions 实现 Embedder。
func (l *Limiter) Dimensions(model string) int { return l.inner.Dimensions(model) }

// Embed 实现 Embedder。
//
// 按 BatchSize 切批后逐批发送，每批失败按指数退避重试。
// 任一批最终失败则整体返回错误——调用方据此降级为关键词检索；
// 已成功的批次不会回滚，但向量写入是幂等的（主键去重），重跑安全。
//
// **首片失败即中止**：后面的分片几乎必然同样失败，逐片重试只会
// 让用户干等十几秒。检索是交互式操作，宁可快速降级。
func (l *Limiter) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	if len(texts) == 0 {
		return nil, Usage{}, nil
	}

	out := make([][]float32, 0, len(texts))
	var total Usage

	for start := 0; start < len(texts); start += l.batches {
		end := min(start+l.batches, len(texts))

		vecs, usage, err := l.embedBatch(ctx, model, texts[start:end])
		if err != nil {
			return nil, total, err
		}
		out = append(out, vecs...)
		total.InputTokens += usage.InputTokens
		total.Requests += usage.Requests
		total.Model = usage.Model
	}

	if err := l.meter.RecordEmbed(ctx, l.backend, model, total); err != nil {
		// 记账失败不能影响检索结果：向量已经拿到，只是成本统计少一笔
		_ = err
	}
	return out, total, nil
}

// embedBatch 发一批并重试。
//
// 重试预算用**总时长**而非次数：服务端慢响应与快速失败的重试代价
// 差一个数量级，只数次数无法控制用户等待时间。
func (l *Limiter) embedBatch(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	var vecs [][]float32
	var usage Usage
	err := l.retry(ctx, func() error {
		var err error
		vecs, usage, err = l.inner.Embed(ctx, model, texts)
		return err
	})
	if err != nil {
		return nil, Usage{}, err
	}
	return vecs, usage, nil
}

// Rerank 实现 Reranker。
func (l *Limiter) Rerank(ctx context.Context, model, query string, docs []string) ([]float32, Usage, error) {
	if l.rerank == nil {
		return nil, Usage{}, fmt.Errorf("%w: 该后端不支持重排", ErrNotConfigured)
	}
	if len(docs) == 0 {
		return nil, Usage{}, nil
	}

	var out []float32
	var usage Usage
	err := l.retry(ctx, func() error {
		var err error
		out, usage, err = l.rerank.Rerank(ctx, model, query, docs)
		return err
	})
	if err != nil {
		return nil, Usage{}, err
	}
	_ = l.meter.RecordRerank(ctx, l.backend, model, usage)
	return out, usage, nil
}

// retry 执行 fn，遇到可重试错误时按指数退避重试。
//
// 嵌入与重排共用这一个重试器：两处策略必须一致，否则同一次故障
// 下一条路径放弃、另一条还在退避，用户看到的行为不可预测。
//
// 预算用**总时长**而非次数：服务端慢响应与快速失败的重试代价差
// 一个数量级，只数次数无法控制用户等待时间。
func (l *Limiter) retry(ctx context.Context, fn func() error) error {
	var lastErr error
	backoff := l.backoff
	deadline := time.Now().Add(l.retryBudget)

	for attempt := 0; attempt <= l.maxRetries; attempt++ {
		if err := l.wait(ctx); err != nil {
			return err
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			// 只重试暂时性错误。配置错误、认证失败重试一万次也没用。
			if !isRetryable(err) {
				return err
			}
		}
		if attempt == l.maxRetries || time.Now().Add(backoff).After(deadline) {
			break
		}
		if !sleepCtx(ctx, backoff) {
			return ctx.Err()
		}
		backoff *= 2
	}
	return lastErr
}

// HasReranker 报告是否有可用的重排后端。
func (l *Limiter) HasReranker() bool { return l.rerank != nil }

func (l *Limiter) wait(ctx context.Context) error {
	if l.limiter == nil {
		return nil
	}
	return l.limiter.Wait(ctx)
}

// isRetryable 报告错误是否值得重试。
//
// 只有 ErrUnavailable 重试有意义；其中 ErrUnreachable（连接被拒、
// DNS 失败）再单独排除——服务没启动时重试只是让用户白等十秒。
// 配置错误、认证失败、模型不存在同理。
func isRetryable(err error) bool {
	// 输入问题重试多少次都一样
	if errors.Is(err, ErrUnsupportedInput) || errors.Is(err, ErrNotConfigured) {
		return false
	}
	// 连不上：服务没起或地址配错，别让用户干等退避
	if errors.Is(err, ErrUnreachable) {
		return false
	}
	return errors.Is(err, ErrUnavailable)
}

// sleepCtx 等待 d 或 ctx 取消，返回是否等满。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

var _ Embedder = (*Limiter)(nil)
var _ Reranker = (*Limiter)(nil)
