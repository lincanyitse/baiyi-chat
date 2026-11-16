package embed

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubEmbedder 是可编程的测试替身。
type stubEmbedder struct {
	calls     atomic.Int32
	failFirst int32 // 前 N 次调用返回 ErrUnavailable
	failKind  error
	dims      int
}

func (s *stubEmbedder) Name() string          { return "stub" }
func (s *stubEmbedder) Dimensions(string) int { return s.dims }
func (s *stubEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	n := s.calls.Add(1)
	if s.failKind != nil && n <= s.failFirst {
		return nil, Usage{}, s.failKind
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, Usage{InputTokens: len(texts) * 3, Requests: 1}, nil
}

func TestLimiterSplitsBatches(t *testing.T) {
	stub := &stubEmbedder{}
	l := NewLimiter(stub, nil, LimiterOptions{BatchSize: 10})

	texts := make([]string, 25)
	vecs, usage, err := l.Embed(context.Background(), "m", texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 25 {
		t.Fatalf("向量数错误：%d", len(vecs))
	}
	if got := stub.calls.Load(); got != 3 {
		t.Fatalf("25 条按 10 切批应调用 3 次，实际 %d", got)
	}
	if usage.InputTokens != 75 || usage.Requests != 3 {
		t.Fatalf("用量应跨批累加：%+v", usage)
	}
}

func TestLimiterRetriesTransientErrors(t *testing.T) {
	stub := &stubEmbedder{failFirst: 2, failKind: ErrUnavailable}
	l := NewLimiter(stub, nil, LimiterOptions{
		BatchSize: 10, MaxRetries: 3, Backoff: time.Millisecond,
	})

	vecs, _, err := l.Embed(context.Background(), "m", []string{"a"})
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("向量数错误：%d", len(vecs))
	}
	if got := stub.calls.Load(); got != 3 {
		t.Fatalf("应重试 2 次共 3 次调用，实际 %d", got)
	}
}

func TestLimiterDoesNotRetryNonRetryable(t *testing.T) {
	stub := &stubEmbedder{failFirst: 100, failKind: ErrNotConfigured}
	l := NewLimiter(stub, nil, LimiterOptions{
		MaxRetries: 5, Backoff: time.Millisecond,
	})

	_, _, err := l.Embed(context.Background(), "m", []string{"a"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("应原样返回配置错误，实际 %v", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("不可重试的错误不该重试，实际调用 %d 次", got)
	}
}

func TestLimiterGivesUpAfterMaxRetries(t *testing.T) {
	stub := &stubEmbedder{failFirst: 1000, failKind: ErrUnavailable}
	l := NewLimiter(stub, nil, LimiterOptions{
		MaxRetries: 2, Backoff: time.Millisecond,
	})

	_, _, err := l.Embed(context.Background(), "m", []string{"a"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("应返回最后一次错误，实际 %v", err)
	}
	if got := stub.calls.Load(); got != 3 {
		t.Fatalf("1 次首发 + 2 次重试 = 3，实际 %d", got)
	}
}

func TestLimiterRespectsContextCancel(t *testing.T) {
	stub := &stubEmbedder{failFirst: 1000, failKind: ErrUnavailable}
	l := NewLimiter(stub, nil, LimiterOptions{
		MaxRetries: 100, Backoff: 50 * time.Millisecond, RetryBudget: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, _, err := l.Embed(ctx, "m", []string{"a"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
}

func TestLimiterRetryBudgetBoundsWait(t *testing.T) {
	// 服务不可用且退避会超出预算：应快速返回，而不是干等
	stub := &stubEmbedder{failFirst: 1000, failKind: ErrUnavailable}
	l := NewLimiter(stub, nil, LimiterOptions{
		MaxRetries: 100, Backoff: time.Second, RetryBudget: 200 * time.Millisecond,
	})

	start := time.Now()
	_, _, err := l.Embed(context.Background(), "m", []string{"a"})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("应返回 ErrUnavailable，实际 %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("应在重试预算内返回，实际耗时 %v", elapsed)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("预算不足时不该重试，实际调用 %d 次", got)
	}
}

func TestLimiterRecordsUsage(t *testing.T) {
	meter := &recordingMeter{}
	l := NewLimiter(&stubEmbedder{}, nil, LimiterOptions{BatchSize: 5, Meter: meter})

	_, _, err := l.Embed(context.Background(), "m", make([]string, 12))
	if err != nil {
		t.Fatal(err)
	}
	if meter.embeds.Load() != 1 || meter.tokens.Load() != 36 {
		t.Fatalf("记账应只上报一次汇总：calls=%d tokens=%d", meter.embeds.Load(), meter.tokens.Load())
	}
}

func TestLimiterMeterFailureDoesNotFailEmbed(t *testing.T) {
	l := NewLimiter(&stubEmbedder{}, nil, LimiterOptions{Meter: failingMeter{}})
	if _, _, err := l.Embed(context.Background(), "m", []string{"a"}); err != nil {
		t.Fatalf("记账失败不该影响检索：%v", err)
	}
}

func TestLimiterEmptyInput(t *testing.T) {
	stub := &stubEmbedder{}
	l := NewLimiter(stub, nil, LimiterOptions{})
	vecs, usage, err := l.Embed(context.Background(), "m", nil)
	if err != nil || vecs != nil || usage.Requests != 0 {
		t.Fatalf("空输入应直接返回：%v %v %+v", vecs, usage, err)
	}
	if stub.calls.Load() != 0 {
		t.Fatal("空输入不该调用后端")
	}
}

func TestLimiterRerankWithoutBackend(t *testing.T) {
	l := NewLimiter(&stubEmbedder{}, nil, LimiterOptions{})
	_, _, err := l.Rerank(context.Background(), "m", "q", []string{"d"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("无重排后端时应返回 ErrNotConfigured，实际 %v", err)
	}
	if l.HasReranker() {
		t.Fatal("HasReranker 应返回 false")
	}
}

func TestLimiterRerankDelegates(t *testing.T) {
	meter := &recordingMeter{}
	l := NewLimiter(&stubEmbedder{}, &FakeReranker{}, LimiterOptions{Meter: meter})
	if !l.HasReranker() {
		t.Fatal("HasReranker 应返回 true")
	}

	scores, _, err := l.Rerank(context.Background(), "m", "白泽", []string{"白泽助理", "无关内容"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores[0] <= scores[1] {
		t.Fatalf("重排结果错误：%v", scores)
	}
	if meter.reranks.Load() != 1 {
		t.Fatalf("重排用量未上报：%d", meter.reranks.Load())
	}
}

type recordingMeter struct {
	embeds  atomic.Int32
	reranks atomic.Int32
	tokens  atomic.Int64
}

func (m *recordingMeter) RecordEmbed(_ context.Context, _, _ string, u Usage) error {
	m.embeds.Add(1)
	m.tokens.Add(int64(u.InputTokens))
	return nil
}

func (m *recordingMeter) RecordRerank(context.Context, string, string, Usage) error {
	m.reranks.Add(1)
	return nil
}

type failingMeter struct{}

func (failingMeter) RecordEmbed(context.Context, string, string, Usage) error {
	return errors.New("磁盘满")
}
func (failingMeter) RecordRerank(context.Context, string, string, Usage) error {
	return errors.New("磁盘满")
}
