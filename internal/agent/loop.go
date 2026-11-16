package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// Engine 执行一轮完整对话。
type Engine struct {
	store   Store
	log     *slog.Logger
	clock   func() time.Time
	outputs tool.OutputStore
}

// Options 是 Engine 的构造参数。
type Options struct {
	Store   Store
	Log     *slog.Logger
	Clock   func() time.Time
	Outputs tool.OutputStore
}

// New 构造 Engine。
func New(opts Options) *Engine {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := opts.Log
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: opts.Store, log: logger, clock: clock, outputs: opts.Outputs}
}

// StreamCallback 在流式过程中被调用。
type StreamCallback func(ev llm.Event)

// Run 执行一轮对话，直到模型给出最终答复、挂起或失败。
func (e *Engine) Run(ctx context.Context, in TurnInput, cb StreamCallback) (Result, error) {
	turn := &turnState{
		Engine:    e,
		in:        in,
		budget:    in.Budget.withDefaults(),
		cb:        cb,
		started:   e.clock(),
		assembler: contextx.New(),
		toolSpecs: toolSpecs(in.Tools),
		taint:     &tool.TaintState{},
	}
	return turn.run(ctx)
}

// turnState 是一轮对话的可变状态。
type turnState struct {
	*Engine
	in     TurnInput
	budget Budget

	messages  []llm.Message
	pending   []llm.ToolUseBlock // 本轮待执行的工具调用
	text      strings.Builder    // 当前步的文本累积
	thinking  strings.Builder
	usage     llm.UsageEvent
	lastStop  llm.StopReason
	firstTok  time.Time
	stepStart time.Time
	steps     int
	toolCalls int
	// executed 记录本轮真正执行过的工具调用，用于落库与展示。
	executed []llm.ToolUseBlock
	started  time.Time
	cb       StreamCallback
	// taint 记录本轮是否消费过外部不可信内容，影响审批策略。
	taint *tool.TaintState
	// assembler 为 nil 时退回「原样发送历史」的简单路径。
	assembler *contextx.Assembler
	// toolSpecs 是工具声明的缓存：整轮不变，而 MCP 工具的 Spec()
	// 每次调用都要 json.Marshal schema，每步算两次纯属浪费。
	toolSpecs []llm.ToolSpec
	// historyLen 是「用户输入」在 messages 中的下标。
	// 历史段 = messages[:historyLen]，当前输入及其后的工具往返 = messages[historyLen:]。
	historyLen int
}

func (t *turnState) run(ctx context.Context) (Result, error) {
	// 整轮墙钟预算
	ctx, cancel := context.WithTimeout(ctx, t.budget.TurnTimeout)
	defer cancel()

	t.messages = make([]llm.Message, 0, len(t.in.History)+4)
	t.messages = append(t.messages, t.in.History...)
	t.messages = append(t.messages, llm.NewText(llm.RoleUser, t.in.UserText))
	// historyLen 标记「用户输入之后新增的消息」起点。
	// 组装器需要把历史与当前输入分开处理：当前输入永不裁剪。
	t.historyLen = len(t.in.History)

	st := stIngest
	for st != stDone {
		select {
		case <-ctx.Done():
			return t.finish(ctx, llm.StopError, ctx.Err()), ctx.Err()
		default:
		}

		var next state
		var err error
		switch st {
		case stIngest:
			next = stAssemble
		case stAssemble:
			next = stInfer
		case stInfer:
			next, err = t.infer(ctx)
		case stDecide:
			next, err = t.decide()
		case stExecute:
			next, err = t.execute(ctx)
		case stRefill:
			next = stInfer
		case stFinalize:
			next = stDone
		default:
			err = fmt.Errorf("非法状态 %v", st)
		}
		if err != nil {
			return t.finish(ctx, llm.StopError, err), err
		}
		if !canTransition(st, next) && next != stDone {
			return t.finish(ctx, llm.StopError,
				fmt.Errorf("非法状态转移 %v → %v", st, next)), nil
		}
		st = next
	}

	return t.finish(ctx, t.lastStop, nil), nil
}

// infer 执行一次模型调用并消费事件流。
func (t *turnState) infer(ctx context.Context) (state, error) {
	if t.steps >= t.budget.MaxSteps {
		return stFinalize, nil
	}
	t.steps++
	t.stepStart = t.clock()
	t.text.Reset()
	t.thinking.Reset()
	t.pending = nil
	t.firstTok = time.Time{}

	stepCtx, cancel := context.WithTimeout(ctx, t.budget.StepTimeout)
	defer cancel()

	caps := t.in.Provider.Capabilities(t.in.Model)

	// 上下文组装：八段分层、预算分配、超限降级。
	// 每步都重新组装，因为工具结果会显著改变历史体积。
	var (
		system   = t.in.System
		messages = t.messages
		breaks   []int
	)
	if t.assembler != nil {
		// 历史 = 本轮开始之前的消息，可以按预算裁剪。
		// 本轮已产生的消息（用户输入、助手回复、工具往返）必须完整保留，
		// 否则会破坏 tool_use/tool_result 配对，模型直接 400。
		history := t.messages[:t.historyLen]
		live := t.messages[t.historyLen:]

		userInput := llm.NewText(llm.RoleUser, t.in.UserText)
		if len(live) > 0 {
			userInput = live[0]
		}

		out := t.assembler.Assemble(contextx.Input{
			Model:     t.in.Model,
			Caps:      caps,
			Identity:  t.in.System,
			ToolSpecs: t.toolSpecs,
			// 技能的 L0 目录进入稳定前缀段（与工具清单同段）。
			SkillLines: t.in.SkillLines,
			History:    history,
			UserInput:  userInput,
			// 本轮已产生的工具往返作为「必须保留的历史」交给组装器，
			// 这样快照能如实反映模型实际看到的全部内容。
			LiveTurns:   live[1:],
			Summary:     t.in.Summary,
			Notes:       t.in.Notes,
			ProfileCard: t.in.ProfileCard,
			Episodes:    t.in.Episodes,
			RAGChunks:   t.in.RAGChunks,
		})
		system = out.System
		messages = out.Messages
		breaks = out.CacheBreakpoints
		// 快照落库失败不影响本轮执行——它只是诊断材料。
		if t.in.Snapshots != nil {
			if err := t.in.Snapshots.SaveSnapshot(ctx, t.in.TurnID, t.steps, t.in.Model, &out); err != nil {
				t.log.Warn("保存上下文快照失败", "turn", t.in.TurnID, "step", t.steps, "err", err)
			}
		}
	}

	req := llm.Request{
		Model:            t.in.Model,
		System:           system,
		Messages:         messages,
		Stream:           true,
		ToolChoice:       t.in.ToolChoice,
		CacheBreakpoints: breaks,
	}
	if caps.MaxOutput > 0 {
		req.MaxTokens = caps.MaxOutput
	}
	req.Tools = t.toolSpecs

	// 降级链：首选失败且错误可降级时，按顺序换下一个候选。
	events, err := t.openStream(stepCtx, req)
	if err != nil {
		return stFinalize, llm.AsError(err)
	}

	var streamErr *llm.Error
	for ev := range events {
		if t.cb != nil {
			t.cb(ev)
		}
		switch ev := ev.(type) {
		case llm.TextDeltaEvent:
			if t.firstTok.IsZero() {
				t.firstTok = t.clock()
			}
			t.text.WriteString(ev.Text)
		case llm.ThinkingDeltaEvent:
			if t.firstTok.IsZero() {
				t.firstTok = t.clock()
			}
			t.thinking.WriteString(ev.Text)
		case llm.ToolCallReadyEvent:
			if ev.ParseErr != "" {
				// 参数残缺：不执行，转成错误结果让模型重发。
				t.pending = append(t.pending, llm.ToolUseBlock{
					Type: string(llm.BlockToolUse),
					ID:   ev.ID,
					Name: ev.Name,
					Input: json.RawMessage(fmt.Sprintf(
						`{"__error__":%q}`, ev.ParseErr)),
				})
				continue
			}
			t.pending = append(t.pending, llm.NewToolUseBlock(ev.ID, ev.Name, ev.Input))
		case llm.UsageEvent:
			t.usage.Input += ev.Input
			t.usage.Output += ev.Output
			t.usage.Cached += ev.Cached
			t.usage.Reasoning += ev.Reasoning
		case llm.StopEvent:
			t.lastStop = ev.Reason
		case llm.ErrorEvent:
			streamErr = ev.Err
		}
	}

	t.recordUsage(ctx, streamErr)

	if streamErr != nil {
		return stFinalize, streamErr
	}

	// 把本步的助手消息追加进上下文
	blocks := make([]llm.Block, 0, 2+len(t.pending))
	if t.thinking.Len() > 0 {
		blocks = append(blocks, llm.NewThinkingBlock(t.thinking.String(), ""))
	}
	if t.text.Len() > 0 {
		blocks = append(blocks, llm.NewTextBlock(t.text.String()))
	}
	for _, call := range t.pending {
		blocks = append(blocks, call)
	}
	if len(blocks) > 0 {
		t.messages = append(t.messages, llm.Message{
			Role: llm.RoleAssistant, Content: blocks,
		})
	}

	return stDecide, nil
}

// decide 依据停止原因决定下一步。
func (t *turnState) decide() (state, error) {
	if len(t.pending) == 0 {
		return stFinalize, nil
	}
	if t.lastStop != llm.StopToolUse && t.lastStop != llm.StopEnd {
		// 截断或过滤导致的工具调用不完整，直接收尾
		return stFinalize, nil
	}
	if t.toolCalls+len(t.pending) > t.budget.MaxToolCalls {
		return stFinalize, nil
	}
	return stExecute, nil
}

// execute 执行本轮全部工具调用。
//
// 并行还是串行由 executeCalls 依据能力声明决定；
// 需要人工审批的调用会在执行前统一挂起整轮。
func (t *turnState) execute(ctx context.Context) (state, error) {
	calls := t.pending
	t.pending = nil
	t.toolCalls += len(calls)
	t.executed = append(t.executed, calls...)

	// 审批门：同批调用一起确认（模型是同批决定的，视为互相独立）。
	if needed, ok := t.needsApproval(calls); ok {
		var decision tool.ApprovalDecision
		if t.in.Approver == nil {
			// 无人可问：拒绝而不是放行。宁可这一轮少做一件事，
			// 也不能让危险操作在无人值守时静默执行。
			decision = tool.ApprovalDecision{
				Action: tool.Deny,
				Reason: "当前渠道不支持审批，无法执行需要确认的操作",
			}
		} else {
			var err error
			decision, err = t.in.Approver.Request(ctx, tool.ApprovalRequest{
				TurnID:    t.in.TurnID,
				SessionID: t.in.SessionID,
				Calls:     needed,
				ExpiresAt: t.clock().Add(approvalTimeout),
			})
			if err != nil {
				// 审批失败（超时/取消）按拒绝处理，把原因回填给模型
				decision = tool.ApprovalDecision{Action: tool.Deny, Reason: err.Error()}
			}
		}
		if !decision.Approved() {
			reason := decision.Reason
			if reason == "" {
				reason = "用户拒绝了该操作"
			}
			results := make([]llm.Block, 0, len(calls))
			for _, call := range calls {
				results = append(results, errorBlock(call.ID, reason))
			}
			t.messages = append(t.messages, llm.Message{
				Role: llm.RoleUser, Content: results,
			})
			return stRefill, nil
		}
		if t.in.MarkApproved != nil {
			for _, c := range needed {
				t.in.MarkApproved(c.Name, decision.Action)
			}
		}
	}

	results := t.executeCalls(ctx, calls)
	t.messages = append(t.messages, llm.Message{
		Role: llm.RoleUser, Content: results,
	})
	return stRefill, nil
}

// runTool 执行单个工具并返回可回填的结果块。
func (t *turnState) runTool(ctx context.Context, call llm.ToolUseBlock) llm.Block {
	argsHash := hashArgs(call.Input)

	// 幂等缓存：同一调用重放时直接命中，不重复执行。
	if t.store != nil {
		if cached, ok, _ := t.store.ToolResult(ctx, t.in.TurnID, call.ID); ok {
			return llm.NewToolResultBlock(call.ID,
				[]llm.Block{llm.NewTextBlock(cached.ResultJSON)}, cached.IsError)
		}
	}

	// 早期失败也走同一条收尾路径，保证幂等缓存不遗漏。
	var (
		finalText string
		isError   bool
		handle    string
		duration  time.Duration
	)

	tl, ok := findTool(t.in.Tools, call.Name)
	if !ok {
		finalText, isError = fmt.Sprintf("错误: 未知工具 %q", call.Name), true
		return t.recordToolResult(ctx, call, finalText, isError, handle, duration, argsHash)
	}

	// 参数残缺（由 infer 标记）直接回填错误
	if strings.Contains(string(call.Input), "__error__") {
		finalText, isError = "错误: 工具参数不完整，请重新调用", true
		return t.recordToolResult(ctx, call, finalText, isError, handle, duration, argsHash)
	}

	spec := tl.Spec()
	execCtx := &tool.ExecContext{
		SessionID: t.in.SessionID,
		TurnID:    t.in.TurnID,
		CallID:    call.ID,
		Approver:  t.in.Approver,
		Taint:     t.taint,
		Outputs:   t.outputs,
		Clock:     t.clock,
		Log:       t.log,
	}

	toolCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	start := t.clock()
	res, execErr := tl.Execute(toolCtx, call.Input, execCtx)
	duration = t.clock().Sub(start)

	if execErr != nil {
		res = tool.ErrorResult("工具执行失败: " + execErr.Error())
	}

	truncated := tool.TruncateResult(res.Text(), tool.TruncateOptions{})
	if truncated.DidTruncate && t.outputs != nil {
		if h, saveErr := t.outputs.Save(ctx, t.in.TurnID, call.ID, truncated.Full); saveErr == nil {
			handle = h
		}
	}

	final := tool.ApplyTruncation(truncated, t.outputs, handle)
	finalText = final.Text()
	isError = res.IsError
	if isError && !strings.HasPrefix(finalText, "错误") {
		finalText = "错误: " + finalText
	}

	t.log.Info("工具执行",
		"turn", t.in.TurnID, "tool", call.Name,
		"is_error", isError, "duration_ms", duration.Milliseconds(),
		"truncated", truncated.DidTruncate)

	return t.recordToolResult(ctx, call, finalText, isError, handle, duration, argsHash)
}

// recordToolResult 写入幂等缓存并构造回填块。
//
// 所有退出路径（未知工具、参数残缺、正常执行、执行失败）都经过这里，
// 保证「同一 call id 绝不重复执行」这条不变量不因某条分支而被破坏。
func (t *turnState) recordToolResult(
	ctx context.Context, call llm.ToolUseBlock,
	text string, isError bool, handle string, duration time.Duration, argsHash string,
) llm.Block {
	if t.store != nil {
		if err := t.store.SaveToolResult(ctx, StoredToolResult{
			TurnID: t.in.TurnID, CallID: call.ID, Name: call.Name,
			ArgsHash: argsHash, ResultJSON: text,
			IsError: isError, Handle: handle,
			DurationMS: duration.Milliseconds(),
		}); err != nil {
			t.log.Error("保存工具结果失败", "turn", t.in.TurnID, "call", call.ID, "err", err)
		}
	}
	return llm.NewToolResultBlock(call.ID, []llm.Block{llm.NewTextBlock(text)}, isError)
}

// finish 组装最终结果。
func (t *turnState) finish(_ context.Context, reason llm.StopReason, err error) Result {
	res := Result{
		TurnID:     t.in.TurnID,
		Text:       t.text.String(),
		Thinking:   t.thinking.String(),
		ToolCalls:  t.executed,
		Usage:      t.usage,
		Latency:    t.clock().Sub(t.started),
		StopReason: reason,
		Steps:      t.steps,
		Err:        err,
	}
	if !t.firstTok.IsZero() {
		res.TTFT = t.firstTok.Sub(t.stepStart)
	}
	return res
}

// recordUsage 上报一次模型调用的用量。
func (t *turnState) recordUsage(ctx context.Context, err *llm.Error) {
	if t.store == nil {
		return
	}
	var kind llm.ErrorKind
	if err != nil {
		kind = err.Kind
	}
	var ttft time.Duration
	if !t.firstTok.IsZero() {
		ttft = t.firstTok.Sub(t.stepStart)
	}
	_ = t.store.RecordUsage(ctx, t.in.TurnID,
		t.in.Provider.Name(), t.in.Model, t.steps,
		t.usage, t.clock().Sub(t.stepStart), ttft, kind)
}

func toolSpecs(tools []tool.Tool) []llm.ToolSpec {
	if len(tools) == 0 {
		return nil
	}
	out := make([]llm.ToolSpec, 0, len(tools))
	for _, tl := range tools {
		s := tl.Spec()
		out = append(out, llm.ToolSpec{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}
	return out
}

func findTool(tools []tool.Tool, name string) (tool.Tool, bool) {
	for _, tl := range tools {
		if tl.Spec().Name == name {
			return tl, true
		}
	}
	return nil, false
}

func hashArgs(in json.RawMessage) string {
	sum := sha256.Sum256(in)
	return hex.EncodeToString(sum[:8])
}

func errorBlock(callID, msg string) llm.Block {
	return llm.NewToolResultBlock(callID,
		[]llm.Block{llm.NewTextBlock("错误: " + msg)}, true)
}

// openStream 尝试降级链上的候选，返回成功建立的事件流。
//
// 只在「还没开始输出」时降级——流中错误不在此重试，见架构 §4.6。
// 每个候选的结果都上报给 session，用于健康度统计。
func (t *turnState) openStream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	tryOne := func(p llm.Provider, model string) (<-chan llm.Event, error) {
		r := req
		r.Model = model
		ch, err := p.Stream(ctx, r)
		if err != nil {
			e := llm.AsError(err)
			if t.in.ReportResult != nil {
				t.in.ReportResult(p.Name(), e)
			}
			return nil, e
		}
		if t.in.ReportResult != nil {
			t.in.ReportResult(p.Name(), nil)
		}
		return ch, nil
	}

	ch, err := tryOne(t.in.Provider, t.in.Model)
	if err == nil {
		return ch, nil
	}

	e := llm.AsError(err)
	if !e.Fallbackable() {
		return nil, err
	}

	for _, fb := range t.in.Fallbacks {
		t.log.Warn("主 Provider 失败，尝试降级",
			"failed_provider", t.in.Provider.Name(), "kind", e.Kind,
			"fallback", fb.Provider.Name(), "model", fb.Model)

		ch, fbErr := tryOne(fb.Provider, fb.Model)
		if fbErr == nil {
			// 降级成功：后续步骤都用这个候选
			t.in.Provider = fb.Provider
			t.in.Model = fb.Model
			return ch, nil
		}
		e = llm.AsError(fbErr)
		if !e.Fallbackable() {
			return nil, e
		}
	}
	return nil, e
}
