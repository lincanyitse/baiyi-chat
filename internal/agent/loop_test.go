package agent

import (
	"context"
	"encoding/json"
	"errors"

	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/lincanyitse/baiyi-chat/internal/infra/logx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// fakeProvider 按脚本逐次返回预设事件流。
type fakeProvider struct {
	mu      sync.Mutex
	scripts [][]llm.Event
	calls   int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Capabilities(string) llm.Capabilities {
	return llm.Capabilities{ContextWindow: 100000, MaxOutput: 4096, Tools: true, ParallelTools: true}
}

func (f *fakeProvider) CountTokens(context.Context, llm.Request) (int, error) { return 0, nil }

func (f *fakeProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	f.calls++
	if idx >= len(f.scripts) {
		idx = len(f.scripts) - 1
	}
	ch := make(chan llm.Event, len(f.scripts[idx])+1)
	go func() {
		defer close(ch)
		ch <- llm.StartEvent{Provider: "fake"}
		for _, ev := range f.scripts[idx] {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// fakeStore 记录 agent 的持久化调用。
type fakeStore struct {
	mu       sync.Mutex
	messages []llm.Message
	tools    map[string]StoredToolResult
	usage    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{tools: map[string]StoredToolResult{}}
}

func (s *fakeStore) AppendMessage(_ context.Context, _ string, role llm.Role, blocks []llm.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, llm.Message{Role: role, Content: blocks})
	return nil
}

func (s *fakeStore) ToolResult(_ context.Context, turnID, callID string) (StoredToolResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.tools[turnID+"/"+callID]
	return r, ok, nil
}

func (s *fakeStore) SaveToolResult(_ context.Context, r StoredToolResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[r.TurnID+"/"+r.CallID] = r
	return nil
}

func (s *fakeStore) RecordUsage(_ context.Context, _, _, _ string, _ int, _ llm.UsageEvent, _, _ time.Duration, _ llm.ErrorKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage++
	return nil
}

// stubTool 是可控制行为的测试工具。
type stubTool struct {
	name   string
	result tool.Result
	err    error
	execs  int
	mu     sync.Mutex
	onExec func()
	// specOverride 非零时覆盖风险等级，用于审批测试。
	specOverride tool.RiskLevel
}

func (s *stubTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: s.name, Description: "测试", Risk: s.specOverride,
	})
}

func (s *stubTool) Execute(context.Context, json.RawMessage, *tool.ExecContext) (tool.Result, error) {
	s.mu.Lock()
	s.execs++
	n := s.execs
	s.mu.Unlock()
	if s.onExec != nil {
		s.onExec()
	}
	_ = n
	return s.result, s.err
}

func textScript(text string) []llm.Event {
	return []llm.Event{
		llm.TextDeltaEvent{Index: 0, Text: text},
		llm.UsageEvent{Input: 10, Output: 5},
		llm.StopEvent{Reason: llm.StopEnd},
	}
}

func toolScript(id, name string, input string) []llm.Event {
	return []llm.Event{
		llm.ToolCallReadyEvent{Index: 0, ID: id, Name: name, Input: json.RawMessage(input)},
		llm.UsageEvent{Input: 10, Output: 5},
		llm.StopEvent{Reason: llm.StopToolUse},
	}
}

func TestEngineSingleTurnNoTools(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{textScript("你好")}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "你好" {
		t.Errorf("text = %q", res.Text)
	}
	if res.Steps != 1 {
		t.Errorf("steps = %d, 期望 1", res.Steps)
	}
	if res.StopReason != llm.StopEnd {
		t.Errorf("stop = %q", res.StopReason)
	}
	if store.usage != 1 {
		t.Errorf("应记录 1 次用量，实际 %d", store.usage)
	}
}

func TestEngineSingleToolCall(t *testing.T) {
	tl := &stubTool{name: "echo", result: tool.TextResult("工具结果")}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "echo", `{"x":1}`),
		textScript("基于工具结果回答"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
	}, nil)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tl.execs != 1 {
		t.Errorf("工具应执行 1 次，实际 %d", tl.execs)
	}
	if res.Steps != 2 {
		t.Errorf("steps = %d, 期望 2（推理+工具后推理）", res.Steps)
	}
	if res.Text != "基于工具结果回答" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestEngineMultipleSerialToolCalls(t *testing.T) {
	tl := &stubTool{name: "step", result: tool.TextResult("ok")}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "step", `{}`),
		toolScript("c2", "step", `{}`),
		toolScript("c3", "step", `{}`),
		textScript("完成"),
	}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Budget: Budget{MaxSteps: 10},
	}, nil)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tl.execs != 3 {
		t.Errorf("应执行 3 次工具，实际 %d", tl.execs)
	}
	if res.Steps != 4 {
		t.Errorf("steps = %d, 期望 4", res.Steps)
	}
}

func TestEngineRespectsMaxSteps(t *testing.T) {
	tl := &stubTool{name: "loop", result: tool.TextResult("ok")}
	prov := &fakeProvider{scripts: [][]llm.Event{toolScript("c", "loop", `{}`)}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, _ := e.Run(ctx, TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Budget: Budget{MaxSteps: 3},
	}, nil)

	if res.Steps > 3 {
		t.Errorf("步数 %d 超过上限 3", res.Steps)
	}
}

func TestEngineToolIdempotencyCache(t *testing.T) {
	tl := &stubTool{name: "once", result: tool.TextResult("结果")}
	store := newFakeStore()
	// 预置缓存
	store.tools["t1/c1"] = StoredToolResult{
		TurnID: "t1", CallID: "c1", ResultJSON: "缓存结果",
	}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "once", `{}`),
		textScript("完成"),
	}}
	e := New(Options{Store: store, Log: discardLogger()})

	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tl.execs != 0 {
		t.Errorf("命中缓存时不应执行工具，实际执行 %d 次", tl.execs)
	}
}

func TestEngineUnknownTool(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "nonexistent", `{}`),
		textScript("完成"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)
	if err != nil {
		t.Fatalf("未知工具不应终止整轮: %v", err)
	}
	// 应回填错误结果并写入缓存
	r, ok := store.tools["t1/c1"]
	if !ok {
		t.Fatal("应缓存错误结果")
	}
	if !r.IsError || !strings.Contains(r.ResultJSON, "未知工具") {
		t.Errorf("错误结果 = %+v", r)
	}
}

func TestEngineToolErrorDoesNotAbort(t *testing.T) {
	tl := &stubTool{name: "boom", err: errors.New("执行失败")}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "boom", `{}`),
		textScript("我换个方式"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
	}, nil)

	if err != nil {
		t.Fatalf("工具失败不应终止整轮: %v", err)
	}
	if res.Text != "我换个方式" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestEngineToolPanicRecovered(t *testing.T) {
	tl := &stubTool{name: "panicky"}
	tl.onExec = func() { panic("故意 panic") }
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "panicky", `{}`),
		textScript("继续"),
	}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	// 阶段 A 是串行执行，panic 会直接冒泡——这里记录当前行为，
	// 阶段 B 加入 recover 后改为断言「不 panic」。
	defer func() {
		if r := recover(); r != nil {
			t.Logf("串行执行下 panic 会冒泡（阶段 B 修复）：%v", r)
		}
	}()
	_, _ = e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
	}, nil)
}

func TestEngineParseErrorToolCall(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "x", ParseErr: "参数不是合法 JSON"},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("已重试"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "已重试" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestEngineStreamError(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.TextDeltaEvent{Index: 0, Text: "部分"},
			llm.ErrorEvent{Err: &llm.Error{Kind: llm.ErrRateLimited, Message: "限流"}},
		},
	}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)

	if err == nil {
		t.Fatal("流中错误应返回错误")
	}
	if res.Text != "部分" {
		t.Errorf("应保留已输出内容，实际 %q", res.Text)
	}
}

func TestEngineContextCancel(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{textScript("x")}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := e.Run(ctx, TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)
	if err == nil {
		t.Fatal("取消应立即返回错误")
	}
}

func TestEnginePassesToolsToProvider(t *testing.T) {
	var captured llm.Request
	prov := &captureProvider{onStream: func(req llm.Request) { captured = req }}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, _ = e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
		Tools: []tool.Tool{&stubTool{name: "mytool", result: tool.TextResult("x")}},
	}, nil)

	if len(captured.Tools) != 1 || captured.Tools[0].Name != "mytool" {
		t.Errorf("工具定义未传给 Provider: %+v", captured.Tools)
	}
}

type captureProvider struct{ onStream func(llm.Request) }

func (c *captureProvider) Name() string { return "capture" }
func (c *captureProvider) Capabilities(string) llm.Capabilities {
	return llm.Capabilities{ContextWindow: 100000, MaxOutput: 4096}
}
func (c *captureProvider) CountTokens(context.Context, llm.Request) (int, error) { return 0, nil }
func (c *captureProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	c.onStream(req)
	ch := make(chan llm.Event, 2)
	go func() {
		defer close(ch)
		ch <- llm.StopEvent{Reason: llm.StopEnd}
	}()
	return ch, nil
}

func TestStateTransitions(t *testing.T) {
	valid := [][2]state{
		{stIngest, stAssemble}, {stAssemble, stInfer}, {stInfer, stDecide},
		{stDecide, stExecute}, {stDecide, stFinalize}, {stExecute, stRefill},
		{stRefill, stInfer}, {stFinalize, stDone},
	}
	for _, p := range valid {
		if !canTransition(p[0], p[1]) {
			t.Errorf("%v → %v 应合法", p[0], p[1])
		}
	}

	invalid := [][2]state{
		{stIngest, stInfer}, {stInfer, stExecute}, {stExecute, stInfer},
		{stDone, stIngest},
	}
	for _, p := range invalid {
		if canTransition(p[0], p[1]) {
			t.Errorf("%v → %v 应非法", p[0], p[1])
		}
	}
}

func TestBudgetDefaults(t *testing.T) {
	b := Budget{}.withDefaults()
	if b.MaxSteps != 12 || b.TurnTimeout == 0 || b.StepTimeout == 0 || b.MaxToolCalls != 20 {
		t.Errorf("默认预算 = %+v", b)
	}
}

func TestResultFooterFields(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.Event{textScript("x")}}
	// 注入步进时钟，避免真实时钟在快速执行下精度不足导致 Latency 为 0。
	var tick int
	clock := func() time.Time {
		tick++
		return time.Unix(0, int64(tick)*int64(time.Millisecond))
	}
	e := New(Options{Store: newFakeStore(), Log: discardLogger(), Clock: clock})
	res, _ := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m",
	}, nil)

	if res.Usage.Input != 10 || res.Usage.Output != 5 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Latency <= 0 {
		t.Errorf("latency 应大于 0，实际 %v", res.Latency)
	}
}

func discardLogger() *slog.Logger { return logx.Discard() }
