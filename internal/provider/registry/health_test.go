package registry

import (
	"context"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

func newTestTracker() *healthTracker {
	h := newHealthTracker()
	// 固定时钟，便于测试冷却窗口
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return base }
	return h
}

func TestHealthInitiallyAvailable(t *testing.T) {
	h := newTestTracker()
	if !h.available("unknown-provider") {
		t.Error("未知 Provider 应视为可用")
	}
}

func TestHealthCooldownAfterFailures(t *testing.T) {
	h := newTestTracker()
	base := h.now()

	for i := 0; i < failuresBeforeCooldown; i++ {
		if !h.available("p") {
			t.Fatalf("第 %d 次失败前不应冷却", i)
		}
		h.recordFailure("p", llm.ErrTransient, "网络错误")
	}

	if h.available("p") {
		t.Error("达到阈值后应进入冷却")
	}

	// 冷却期结束后恢复
	h.now = func() time.Time { return base.Add(cooldownDuration + time.Second) }
	if !h.available("p") {
		t.Error("冷却期结束后应恢复可用")
	}
}

func TestHealthAuthErrorImmediateCooldown(t *testing.T) {
	h := newTestTracker()
	// 密钥问题不是瞬时故障，一次就冷却
	h.recordFailure("p", llm.ErrAuth, "401")

	if h.available("p") {
		t.Error("认证失败应立即冷却，重试无意义")
	}
}

func TestHealthQuotaErrorImmediateCooldown(t *testing.T) {
	h := newTestTracker()
	h.recordFailure("p", llm.ErrQuota, "余额不足")

	if h.available("p") {
		t.Error("配额耗尽应立即冷却")
	}
}

func TestHealthSuccessResets(t *testing.T) {
	h := newTestTracker()
	h.recordFailure("p", llm.ErrTransient, "错误")
	h.recordFailure("p", llm.ErrTransient, "错误")
	h.recordSuccess("p")

	failures, cooling, _ := h.status("p")
	if failures != 0 || cooling {
		t.Errorf("成功应清零，实际 failures=%d cooling=%v", failures, cooling)
	}
}

func TestHealthStatus(t *testing.T) {
	h := newTestTracker()
	h.recordFailure("p", llm.ErrRateLimited, "限流")

	failures, cooling, lastErr := h.status("p")
	if failures != 1 {
		t.Errorf("failures = %d", failures)
	}
	if cooling {
		t.Error("单次限流不应冷却")
	}
	if lastErr != "限流" {
		t.Errorf("lastErr = %q", lastErr)
	}
}

func TestRecordResultIgnoresCancel(t *testing.T) {
	h := newTestTracker()
	// 用户取消不是 Provider 的问题，不应计入失败
	for i := 0; i < 10; i++ {
		h.recordFailure("p", llm.ErrCanceled, "取消")
	}
	// 直接测试 recordResult 的语义在 Registry 层
	r := &Registry{
		providers: map[string]llm.Provider{},
		health:    h,
	}
	r.RecordResult("p", &llm.Error{Kind: llm.ErrCanceled})

	failures, _, _ := h.status("p")
	if failures != 10 {
		t.Errorf("RecordResult 不应为取消累加计数，实际 failures=%d", failures)
	}
}

func TestParseRef(t *testing.T) {
	cases := map[string]struct {
		wantProvider, wantModel string
		wantErr                 bool
	}{
		"p/m":             {"p", "m", false},
		"p/deepseek/chat": {"p", "deepseek/chat", false}, // 模型名可含斜杠
		"nomodel":         {"", "", true},
		"/m":              {"", "", true},
		"p/":              {"", "", true},
	}
	for input, want := range cases {
		got, err := ParseRef(input)
		if want.wantErr {
			if err == nil {
				t.Errorf("%q 应报错", input)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if got.Provider != want.wantProvider || got.Model != want.wantModel {
			t.Errorf("%q → %+v", input, got)
		}
	}
}

func TestModelRefString(t *testing.T) {
	r := ModelRef{Provider: "p", Model: "m"}
	if r.String() != "p/m" {
		t.Errorf("String = %q", r.String())
	}
}

func TestResolveChainSkipsCooling(t *testing.T) {
	// 构造两个候选，让第一个进入冷却
	reg := &Registry{
		providers: map[string]llm.Provider{
			"a": stubProvider{name: "a"},
			"b": stubProvider{name: "b"},
		},
		routes: map[string][]string{
			"chat": {"a/m1", "b/m2"},
		},
		health: newHealthTracker(),
	}

	chain := reg.ResolveChain("chat")
	if len(chain) != 2 {
		t.Fatalf("应有两个候选，实际 %d", len(chain))
	}
	if chain[0].Ref.Provider != "a" || chain[1].Ref.Provider != "b" {
		t.Errorf("顺序 = %s, %s", chain[0].Ref.Provider, chain[1].Ref.Provider)
	}

	// 让 a 冷却
	reg.health.recordFailure("a", llm.ErrAuth, "401")
	chain = reg.ResolveChain("chat")
	if len(chain) != 1 || chain[0].Ref.Provider != "b" {
		t.Errorf("冷却中的 Provider 应被跳过，实际 %+v", chain)
	}
}

type stubProvider struct{ name string }

func (s stubProvider) Name() string                                              { return s.name }
func (s stubProvider) Capabilities(string) llm.Capabilities                      { return llm.Capabilities{} }
func (s stubProvider) CountTokens(_ context.Context, _ llm.Request) (int, error) { return 0, nil }
func (s stubProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	close(ch)
	return ch, nil
}
