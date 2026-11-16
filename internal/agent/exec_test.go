package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// barrierTool 在 Execute 里先报告到达，再等所有同伴到达才返回。
// 串行实现会永远等不到第二个到达者，从而超时失败。
type barrierTool struct {
	name    string
	arrived chan<- string
	release <-chan struct{}
}

func (b *barrierTool) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{Name: b.name, Description: "屏障测试"})
}

func (b *barrierTool) Execute(ctx context.Context, _ json.RawMessage, _ *tool.ExecContext) (tool.Result, error) {
	select {
	case b.arrived <- b.name:
	case <-ctx.Done():
		return tool.ErrorResult("等待同伴超时"), nil
	}
	select {
	case <-b.release:
		return tool.TextResult("ok"), nil
	case <-ctx.Done():
		return tool.ErrorResult("释放超时"), nil
	}
}

func TestParallelToolCallsActuallyParallel(t *testing.T) {
	const n = 3
	arrived := make(chan string, n)
	release := make(chan struct{})

	tools := make([]tool.Tool, 0, n)
	for i := 0; i < n; i++ {
		tools = append(tools, &barrierTool{
			name:    string(rune('a' + i)),
			arrived: arrived,
			release: release,
		})
	}

	// 一次消息里给出 n 个调用
	events := []llm.Event{
		llm.StopEvent{Reason: llm.StopToolUse},
	}
	for i := 0; i < n; i++ {
		events = append([]llm.Event{llm.ToolCallReadyEvent{
			Index: i, ID: string(rune('c' + i)), Name: string(rune('a' + i)),
			Input: json.RawMessage(`{}`),
		}}, events...)
	}

	prov := &fakeProvider{scripts: [][]llm.Event{events, textScript("完成")}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Run(context.Background(), TurnInput{
			TurnID: "t1", SessionID: "s1", UserText: "hi",
			Provider: prov, Model: "m", Tools: tools,
		}, nil)
	}()

	// 等所有工具到达屏障；串行实现只能到达 1 个
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-deadline:
			t.Fatalf("只到达 %d/%d 个工具，说明不是并行执行", i, n)
		}
	}
	close(release)
	<-done
}

func TestSerialWhenCapabilityDisallowsParallel(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0

	tl := &concurrencyProbe{
		onEnter: func() {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
		},
		onExit: func() {
			mu.Lock()
			active--
			mu.Unlock()
		},
	}

	prov := &noParallelProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "probe", Input: json.RawMessage(`{}`)},
			llm.ToolCallReadyEvent{Index: 1, ID: "c2", Name: "probe", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxActive > 1 {
		t.Errorf("能力声明不支持并行时最大并发 = %d，期望 1", maxActive)
	}
}

type concurrencyProbe struct {
	onEnter func()
	onExit  func()
}

func (c *concurrencyProbe) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{Name: "probe", Description: "并发探测"})
}

func (c *concurrencyProbe) Execute(context.Context, json.RawMessage, *tool.ExecContext) (tool.Result, error) {
	c.onEnter()
	time.Sleep(10 * time.Millisecond)
	c.onExit()
	return tool.TextResult("ok"), nil
}

// noParallelProvider 声明不支持并行工具调用。
type noParallelProvider struct{ scripts [][]llm.Event }

func (p *noParallelProvider) Name() string { return "noparallel" }
func (p *noParallelProvider) Capabilities(string) llm.Capabilities {
	return llm.Capabilities{ContextWindow: 100000, MaxOutput: 4096, Tools: true, ParallelTools: false}
}
func (p *noParallelProvider) CountTokens(context.Context, llm.Request) (int, error) { return 0, nil }
func (p *noParallelProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	p.scripts = p.scripts[1:]
	var script []llm.Event
	if len(p.scripts) > 0 {
		script = p.scripts[0]
	}
	ch := make(chan llm.Event, len(script)+1)
	go func() {
		defer close(ch)
		for _, ev := range script {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func TestPanicInOneToolDoesNotBreakOthers(t *testing.T) {
	good := &stubTool{name: "good", result: tool.TextResult("好的")}
	bad := &stubTool{name: "bad"}
	bad.onExec = func() { panic("工具内部爆炸") }

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "bad", Input: json.RawMessage(`{}`)},
			llm.ToolCallReadyEvent{Index: 1, ID: "c2", Name: "good", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("继续"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{bad, good},
	}, nil)

	if err != nil {
		t.Fatalf("单个工具 panic 不应终止整轮: %v", err)
	}
	if res.Text != "继续" {
		t.Errorf("text = %q", res.Text)
	}
	// panic 应被捕获并缓存为错误结果
	if r, ok := store.tools["t1/c1"]; !ok || !r.IsError {
		t.Errorf("panic 的工具应缓存错误结果，实际 %+v ok=%v", r, ok)
	}
	if r, ok := store.tools["t1/c2"]; !ok || r.IsError {
		t.Errorf("正常工具应缓存成功结果，实际 %+v ok=%v", r, ok)
	}
}

func TestApprovalDeniedFillsErrorResults(t *testing.T) {
	tl := &stubTool{name: "danger", result: tool.TextResult("已执行")}
	tl.specOverride = tool.RiskDangerous

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "danger", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("好的，我不做"),
	}}

	approver := &fakeApprover{decision: tool.ApprovalDecision{Action: tool.Deny, Reason: "用户拒绝"}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	res, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver: approver,
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if tl.execs != 0 {
		t.Errorf("被拒绝的工具不应执行，实际 %d", tl.execs)
	}
	if approver.calls != 1 {
		t.Errorf("应请求 1 次审批，实际 %d", approver.calls)
	}
	if res.Text != "好的，我不做" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestApprovalGrantedExecutes(t *testing.T) {
	tl := &stubTool{name: "danger", result: tool.TextResult("已执行")}
	tl.specOverride = tool.RiskDangerous

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "danger", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}

	approver := &fakeApprover{decision: tool.ApprovalDecision{Action: tool.ApproveOnce}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver: approver,
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if tl.execs != 1 {
		t.Errorf("批准后应执行 1 次，实际 %d", tl.execs)
	}
}

func TestReadToolSkipsApproval(t *testing.T) {
	tl := &stubTool{name: "safe", result: tool.TextResult("ok")} // 默认 RiskRead
	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "safe", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}
	approver := &fakeApprover{decision: tool.ApprovalDecision{Action: tool.Deny}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, _ = e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver: approver,
	}, nil)

	if approver.calls != 0 {
		t.Errorf("只读工具不应触发审批，实际请求 %d 次", approver.calls)
	}
	if tl.execs != 1 {
		t.Errorf("只读工具应直接执行，实际 %d", tl.execs)
	}
}

type fakeApprover struct {
	decision tool.ApprovalDecision
	calls    int
}

func (f *fakeApprover) Request(context.Context, tool.ApprovalRequest) (tool.ApprovalDecision, error) {
	f.calls++
	return f.decision, nil
}

func TestApprovalRequestIncludesCalls(t *testing.T) {
	tl := &stubTool{name: "danger", result: tool.TextResult("ok")}
	tl.specOverride = tool.RiskDangerous

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "danger", Input: json.RawMessage(`{"x":1}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}

	var got tool.ApprovalRequest
	approver := &captureApprover{onRequest: func(r tool.ApprovalRequest) { got = r }}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, _ = e.Run(context.Background(), TurnInput{
		TurnID: "t7", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver: approver,
	}, nil)

	if got.TurnID != "t7" {
		t.Errorf("TurnID = %q", got.TurnID)
	}
	if len(got.Calls) != 1 || got.Calls[0].Name != "danger" {
		t.Errorf("calls = %+v", got.Calls)
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Error("应该有过期时间")
	}
}

type captureApprover struct{ onRequest func(tool.ApprovalRequest) }

func (c *captureApprover) Request(_ context.Context, r tool.ApprovalRequest) (tool.ApprovalDecision, error) {
	c.onRequest(r)
	return tool.ApprovalDecision{Action: tool.ApproveOnce}, nil
}

func TestDangerousToolBypassesSessionApproval(t *testing.T) {
	// 危险操作永远确认：即使本会话已授权，也必须再次审批（FR-TOL-002）。
	tl := &stubTool{name: "rm", result: tool.TextResult("已删除")}
	tl.specOverride = tool.RiskDangerous

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "rm", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}

	approver := &fakeApprover{decision: tool.ApprovalDecision{Action: tool.ApproveOnce}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver: approver,
		// 模拟「本会话已永久允许该工具」
		SessionApproved: func(string) bool { return true },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if approver.calls != 1 {
		t.Errorf("危险工具即使已授权也应请求审批，实际请求 %d 次", approver.calls)
	}
}

func TestWriteToolHonorsSessionApproval(t *testing.T) {
	// 写类工具在无污点时可跳过审批
	tl := &stubTool{name: "write", result: tool.TextResult("已写入")}
	tl.specOverride = tool.RiskWrite

	prov := &fakeProvider{scripts: [][]llm.Event{
		{
			llm.ToolCallReadyEvent{Index: 0, ID: "c1", Name: "write", Input: json.RawMessage(`{}`)},
			llm.StopEvent{Reason: llm.StopToolUse},
		},
		textScript("完成"),
	}}

	approver := &fakeApprover{decision: tool.ApprovalDecision{Action: tool.ApproveOnce}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	_, _ = e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "hi",
		Provider: prov, Model: "m", Tools: []tool.Tool{tl},
		Approver:        approver,
		SessionApproved: func(string) bool { return true },
	}, nil)

	if approver.calls != 0 {
		t.Errorf("已授权的写类工具不应重复审批，实际 %d 次", approver.calls)
	}
}

// TestNilApproverDeniesDangerousTool 锁定一个真实漏洞：
// needsApproval 曾在 Approver 为 nil 时直接返回「无需审批」，
// 导致危险工具在无人值守下静默执行（违背 FR-TOL-002）。
func TestNilApproverDeniesDangerousTool(t *testing.T) {
	executed := false
	tl := &stubTool{
		name:         "rm_rf",
		specOverride: tool.RiskDangerous,
		result:       tool.TextResult("已删除"),
		onExec:       func() { executed = true },
	}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "rm_rf", `{"path":"/tmp/x"}`),
		textScript("好的"),
	}}
	store := newFakeStore()
	e := New(Options{Store: store, Log: discardLogger()})

	// 不传 Approver：模拟 Web 渠道漏配审批的情况
	_, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "删掉它",
		Provider: prov, Model: "m",
		Tools:    []tool.Tool{tl},
		Approver: nil,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if executed {
		t.Fatal("Approver 为 nil 时危险工具不该执行")
	}
}

// TestNilApproverAllowsReadOnlyTool 确认只读工具不受影响。
func TestNilApproverAllowsReadOnlyTool(t *testing.T) {
	executed := false
	tl := &stubTool{
		name:         "time_now",
		specOverride: tool.RiskRead,
		result:       tool.TextResult("2026-01-01"),
		onExec:       func() { executed = true },
	}
	prov := &fakeProvider{scripts: [][]llm.Event{
		toolScript("c1", "time_now", `{}`),
		textScript("好的"),
	}}
	e := New(Options{Store: newFakeStore(), Log: discardLogger()})

	if _, err := e.Run(context.Background(), TurnInput{
		TurnID: "t1", SessionID: "s1", UserText: "几点",
		Provider: prov, Model: "m",
		Tools:    []tool.Tool{tl},
		Approver: nil,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("只读工具在无审批时也应执行")
	}
}
