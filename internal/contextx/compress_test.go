package contextx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

type fakeSummaryRepo struct {
	state map[string]SummaryState
}

func newFakeRepo() *fakeSummaryRepo {
	return &fakeSummaryRepo{state: map[string]SummaryState{}}
}

func (f *fakeSummaryRepo) LoadSummary(_ context.Context, sessionID string) (SummaryState, bool, error) {
	st, ok := f.state[sessionID]
	return st, ok, nil
}

func (f *fakeSummaryRepo) SaveSummary(_ context.Context, st SummaryState) error {
	f.state[st.SessionID] = st
	return nil
}

// fakeSummarizer 返回固定结构的摘要。
type fakeSummarizer struct {
	result string
	err    error
	calls  int
}

func (f *fakeSummarizer) Summarize(context.Context, []llm.Message) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if f.result != "" {
		return f.result, nil
	}
	return validSummaryText(), nil
}

func validSummaryText() string {
	var b strings.Builder
	for _, s := range summarySections {
		b.WriteString("## " + s + "\n内容\n\n")
	}
	return b.String()
}

func makeHistory(n int) []llm.Message {
	out := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		out = append(out, llm.NewText(role, "第 "+string(rune('0'+i%10))+" 轮对话内容"))
	}
	return out
}

func TestValidateSummaryRequiresAllSections(t *testing.T) {
	if err := ValidateSummary(validSummaryText()); err != nil {
		t.Fatalf("完整摘要应通过: %v", err)
	}

	// 缺一段就应失败
	for _, sec := range summarySections {
		partial := strings.ReplaceAll(validSummaryText(), "## "+sec, "## 其他")
		if err := ValidateSummary(partial); err == nil {
			t.Errorf("缺少「%s」应报错", sec)
		}
	}

	if err := ValidateSummary(""); err == nil {
		t.Error("空摘要应报错")
	}
}

func TestShouldCompress(t *testing.T) {
	if ShouldCompress(50, 100) {
		t.Error("未到 60% 不应压缩")
	}
	if !ShouldCompress(70, 100) {
		t.Error("超过 60% 应压缩")
	}
	if ShouldCompress(100, 0) {
		t.Error("配额为 0 时不应压缩")
	}
}

func TestCompressKeepsRecentMessages(t *testing.T) {
	repo := newFakeRepo()
	sum := &fakeSummarizer{}
	c := NewCompressor(repo, sum)

	history := makeHistory(40)
	st, cut, err := c.Compress(context.Background(), "s1", history)
	if err != nil {
		t.Fatal(err)
	}

	if cut != len(history)-keepRecentTurns {
		t.Errorf("覆盖边界 = %d, 期望 %d", cut, len(history)-keepRecentTurns)
	}
	if st.CoveredThroughSeq != cut {
		t.Errorf("CoveredThroughSeq = %d", st.CoveredThroughSeq)
	}
	if st.Summary == "" {
		t.Error("摘要不应为空")
	}
	if _, ok := repo.state["s1"]; !ok {
		t.Error("应落库")
	}
}

func TestCompressSkipsShortHistory(t *testing.T) {
	repo := newFakeRepo()
	sum := &fakeSummarizer{}
	c := NewCompressor(repo, sum)

	short := makeHistory(5)
	_, cut, err := c.Compress(context.Background(), "s1", short)
	if err != nil {
		t.Fatal(err)
	}
	if cut != 0 {
		t.Errorf("短历史不应压缩，cut = %d", cut)
	}
	if sum.calls != 0 {
		t.Errorf("不应调用摘要模型，实际 %d 次", sum.calls)
	}
}

func TestCompressRejectsBadSummary(t *testing.T) {
	repo := newFakeRepo()
	// 返回缺少固定结构的摘要
	sum := &fakeSummarizer{result: "这是一段自由发挥的摘要，没有固定结构。"}
	c := NewCompressor(repo, sum)

	_, _, err := c.Compress(context.Background(), "s1", makeHistory(40))
	if err == nil {
		t.Fatal("结构不合规的摘要应被拒绝")
	}
	if _, ok := repo.state["s1"]; ok {
		t.Error("失败的摘要不应落库")
	}
}

func TestCompressPropagatesError(t *testing.T) {
	repo := newFakeRepo()
	sum := &fakeSummarizer{err: errors.New("模型不可用")}
	c := NewCompressor(repo, sum)

	_, _, err := c.Compress(context.Background(), "s1", makeHistory(40))
	if err == nil {
		t.Fatal("摘要失败应返回错误")
	}
}

func TestCompressRecordsMetadata(t *testing.T) {
	repo := newFakeRepo()
	sum := &fakeSummarizer{}
	c := NewCompressor(repo, sum)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.clock = func() time.Time { return fixed }

	st, _, err := c.Compress(context.Background(), "s1", makeHistory(40))
	if err != nil {
		t.Fatal(err)
	}

	if st.TemplateVersion != SummaryTemplateVersion {
		t.Errorf("template version = %q", st.TemplateVersion)
	}
	if st.TokensIn <= 0 {
		t.Error("应记录被压缩的 token 数")
	}
	if !st.UpdatedAt.Equal(fixed) {
		t.Errorf("时间戳 = %v", st.UpdatedAt)
	}
}

func TestSummaryPromptMentionsAllSections(t *testing.T) {
	p := SummaryPrompt()
	for _, s := range summarySections {
		if !strings.Contains(p, s) {
			t.Errorf("提示词应包含「%s」", s)
		}
	}
}

func TestPinnedMessagesPreserved(t *testing.T) {
	// 钉住的消息不应被摘要覆盖——这是 FR-CTX-006 的要求。
	// 当前实现通过「压缩时排除 pinned」体现；这里验证组装器
	// 在历史段里保留 pinned 消息。
	in := Input{
		Caps:      baseCaps(200000),
		UserInput: llm.NewText(llm.RoleUser, "当前问题"),
		History: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.Block{llm.NewTextBlock("重要信息")},
				Meta: llm.MessageMeta{Pinned: true}},
		},
	}
	out := New().Assemble(in)

	var found bool
	for _, m := range out.Messages {
		if strings.Contains(llm.TextOf(m), "重要信息") {
			found = true
		}
	}
	if !found {
		t.Error("钉住的消息应保留在上下文中")
	}
}
