package web

// 跨请求审批。
//
// 问题的形状：agent 在后台 goroutine 里跑到需要确认的工具调用，
// 它必须**阻塞**等用户点按钮；而用户点按钮是一个全新的 HTTP 请求。
// 两者之间没有任何共享的调用栈，只能靠一个内存挂起表桥接。
//
// 不落库：单用户本地场景「重启即重开」可以接受，跨进程恢复审批
// 需要 approvals 表与恢复语义，M3 不做。表已建好，留给 M4。

import (
	"context"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/idgen"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// ApprovalRequest 是一条挂起中的审批，对外暴露给前端。
type ApprovalRequest struct {
	ID        string    `json:"id"`
	TurnID    string    `json:"turn_id"`
	SessionID string    `json:"session_id"`
	ToolName  string    `json:"tool_name"`
	Risk      string    `json:"risk"`
	Summary   string    `json:"summary"`
	Args      string    `json:"args"`
	CreatedAt time.Time `json:"created_at"`
}

type pendingApproval struct {
	req    ApprovalRequest
	calls  []tool.ApprovalCall
	ch     chan tool.ApprovalDecision
	expire time.Time
}

// ApprovalBroker 是 Web 渠道的审批器。
//
// 一个工具调用对应一次审批请求（agent 会逐条问，不去合并同批多个
// 调用——合并 UI 更省点击，但用户无法区分「只放行这一个」，
// 而逐条问的代价在单用户场景下可以接受）。
type ApprovalBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingApproval
	// notify 在新审批出现时关闭并换新，供 SSE 订阅者唤醒
	notify chan struct{}
	// approved 记录用户勾选「本会话允许」的工具
	approved map[string]bool
	// onResolve 在审批被解决时回调，用于落审计日志
	onResolve func(req ApprovalRequest, d tool.ApprovalDecision)
}

// NewApprovalBroker 构造审批器。
func NewApprovalBroker(onResolve func(ApprovalRequest, tool.ApprovalDecision)) *ApprovalBroker {
	return &ApprovalBroker{
		pending:   make(map[string]*pendingApproval),
		notify:    make(chan struct{}),
		approved:  make(map[string]bool),
		onResolve: onResolve,
	}
}

// IsApproved 报告某工具是否已被本会话放行，供 agent 的 SessionApproved。
func (b *ApprovalBroker) IsApproved(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.approved[name]
}

// MarkApproved 记录用户授予的会话级授权，供 agent 的 MarkApproved。
//
// 「拒绝」不记录，否则一次误点拒绝会让该工具在整个会话里都不可用。
func (b *ApprovalBroker) MarkApproved(name string, action tool.ApprovalAction) {
	if action != tool.ApproveSession && action != tool.ApproveForever {
		return
	}
	b.mu.Lock()
	b.approved[name] = true
	b.mu.Unlock()
}

// Request 实现 tool.Approver。它会阻塞直到用户决定、超时或 ctx 取消。
func (b *ApprovalBroker) Request(ctx context.Context, req tool.ApprovalRequest) (tool.ApprovalDecision, error) {
	// 一批里只逐条问第一个需要确认的调用对模型是够的：agent 每轮
	// 只会把「需要确认」的调用送进来，通常只有一条。
	call := req.Calls[0]
	id := idgen.New()

	p := &pendingApproval{
		req: ApprovalRequest{
			ID:        id,
			TurnID:    req.TurnID,
			SessionID: req.SessionID,
			ToolName:  call.Name,
			Risk:      call.Risk.String(),
			Summary:   call.Summary,
			Args:      string(call.Args),
			CreatedAt: time.Now(),
		},
		calls:  req.Calls,
		ch:     make(chan tool.ApprovalDecision, 1),
		expire: req.ExpiresAt,
	}

	b.mu.Lock()
	b.pending[id] = p
	ch := b.notify
	b.notify = make(chan struct{})
	b.mu.Unlock()
	close(ch)

	var timeout <-chan time.Time
	if !req.ExpiresAt.IsZero() {
		t := time.NewTimer(time.Until(req.ExpiresAt))
		defer t.Stop()
		timeout = t.C
	}

	var decision tool.ApprovalDecision
	select {
	case decision = <-p.ch:
	case <-timeout:
		decision = tool.ApprovalDecision{Action: tool.Deny, Reason: "审批超时，已自动拒绝"}
	case <-ctx.Done():
		decision = tool.ApprovalDecision{Action: tool.Deny, Reason: "轮次已取消"}
	}

	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()

	if b.onResolve != nil {
		b.onResolve(p.req, decision)
	}
	return decision, nil
}

// Decide 由 HTTP 层调用，提交用户的决定。
func (b *ApprovalBroker) Decide(id string, d tool.ApprovalDecision) bool {
	b.mu.Lock()
	p, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case p.ch <- d:
		return true
	default:
		// 重复提交：第一条生效，后续忽略
		return false
	}
}

// Pending 返回全部挂起中的审批，供页面刷新后重建弹窗。
func (b *ApprovalBroker) Pending() []ApprovalRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ApprovalRequest, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p.req)
	}
	return out
}

// Notify 返回一个在下次审批出现时关闭的 channel，供 SSE 等待。
func (b *ApprovalBroker) Notify() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.notify
}

// Cancel 取消一条挂起的审批（轮次被停止时调用）。
func (b *ApprovalBroker) Cancel(id string) {
	b.Decide(id, tool.ApprovalDecision{Action: tool.Deny, Reason: "轮次已停止"})
}

// CancelTurn 拒绝某轮次下所有挂起的审批。
func (b *ApprovalBroker) CancelTurn(turnID string) {
	b.mu.Lock()
	var ids []string
	for id, p := range b.pending {
		if p.req.TurnID == turnID {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.Cancel(id)
	}
}

var _ tool.Approver = (*ApprovalBroker)(nil)
