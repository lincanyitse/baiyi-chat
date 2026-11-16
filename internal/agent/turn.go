// Package agent 是 Agent 运行时：状态机、工具编排、预算与取消传播。
//
// 依赖约束（架构纲要 §2.3）：
//   - 只依赖 llm.Provider 接口，不 import 具体适配器
//   - 不 import session；持久化通过消费方定义的 Store 接口注入
package agent

import (
	"context"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// TurnInput 是一轮对话的输入。
type TurnInput struct {
	TurnID    string
	SessionID string
	UserText  string
	// History 是本次之前的完整消息历史（不含本轮输入）。
	History []llm.Message
	// Model 与 Provider 由调用方按路由解析后传入。
	Provider llm.Provider
	Model    string
	// Fallbacks 是降级链上的其余候选（不含首选的 Provider/Model）。
	// 首选失败且错误可降级时按顺序尝试。
	Fallbacks []ProviderChoice
	// ReportResult 上报每次调用结果，供 session 更新 Provider 健康度。
	ReportResult ReportResult
	// System 是稳定前缀里的身份与规则。
	System string
	// Tools 是本轮可用工具。
	Tools []tool.Tool
	// ToolChoice 控制工具调用策略。
	ToolChoice string
	// Approver 处理需要人工确认的工具调用；nil 表示无人值守。
	Approver tool.Approver
	// SessionApproved 报告某工具是否已被本会话永久放行。
	SessionApproved func(name string) bool
	// MarkApproved 在用户批准后记录授权（会话级或永久）。
	MarkApproved func(name string, action tool.ApprovalAction)
	// Summary / Notes / ProfileCard 供上下文组装使用（M1.2 起逐步填充）。
	Summary     string
	Notes       string
	ProfileCard string
	// Episodes 是情节记忆召回结果（M2.2 填充），进当前用户消息。
	Episodes []string
	// RAGChunks 是知识库检索结果（M2.1 预留给热路径预取用）。
	//
	// 实际检索走 rag_search 工具，不走这里——查询嵌入是网络调用，
	// 放进热路径会击穿「本地开销 <100ms」。此字段留给「用户明确要求
	// 带资料回答」这类少量场景。
	RAGChunks []contextx.RAGChunk
	// SkillLines 是技能 L0 目录（名字 + 一句话），进入稳定前缀段。
	SkillLines []string
	// Snapshots 接收每步的上下文快照；nil 表示不持久化。
	Snapshots SnapshotSink
	// Preset 覆盖默认预算（为零值则用默认）。
	Budget Budget
}

// approvalTimeout 是审批挂起的默认超时。
const approvalTimeout = 10 * time.Minute

// Result 是一轮的最终结果。
type Result struct {
	TurnID     string
	Text       string
	Thinking   string
	ToolCalls  []llm.ToolUseBlock
	Usage      llm.UsageEvent
	Cost       float64
	CostKnown  bool
	Latency    time.Duration
	TTFT       time.Duration
	StopReason llm.StopReason
	Steps      int
	// Pending 非空表示本轮挂起，等待用户审批或回答。
	Pending *Pending
	Err     error
}

// PendingKind 是挂起原因。
type PendingKind string

const (
	// PendingApproval 表示等待工具审批。
	PendingApproval PendingKind = "approval"
	// PendingUser 表示等待用户回答 ask_user。
	PendingUser PendingKind = "user"
)

// Pending 描述挂起状态。
type Pending struct {
	Kind       PendingKind
	ApprovalID string
	Calls      []tool.ApprovalCall
	Question   string
	ExpiresAt  time.Time
}

// Budget 是一轮的资源上限。零值字段使用默认值。
type Budget struct {
	MaxSteps       int           // 模型调用次数上限，默认 12
	TurnTimeout    time.Duration // 整轮墙钟上限，默认 180s
	StepTimeout    time.Duration // 单次模型调用上限，默认 120s
	MaxToolCalls   int           // 单轮工具调用总数，默认 20
	MaxCost        float64       // 成本上限，0 表示不限
	MaxTotalTokens int           // token 上限，0 表示不限
}

// withDefaults 填充默认值。
func (b Budget) withDefaults() Budget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = 12
	}
	if b.TurnTimeout <= 0 {
		b.TurnTimeout = 180 * time.Second
	}
	if b.StepTimeout <= 0 {
		b.StepTimeout = 120 * time.Second
	}
	if b.MaxToolCalls <= 0 {
		b.MaxToolCalls = 20
	}
	return b
}

// Store 是 agent 需要的持久化能力，由 session 实现。
//
// 定义在消费方（agent）而非实现方，符合 Go 惯例。
type Store interface {
	// AppendMessage 追加一条消息到 transcript。
	AppendMessage(ctx context.Context, sessionID string, role llm.Role, blocks []llm.Block) error
	// ToolResult 查询幂等缓存。
	ToolResult(ctx context.Context, turnID, callID string) (r StoredToolResult, ok bool, err error)
	// SaveToolResult 写入幂等缓存。
	SaveToolResult(ctx context.Context, r StoredToolResult) error
	// RecordUsage 上报一次模型调用的用量。
	RecordUsage(ctx context.Context, turnID, provider, model string, step int, usage llm.UsageEvent, latency, ttft time.Duration, errKind llm.ErrorKind) error
}

// StoredToolResult 是幂等缓存里的一条工具结果。
type StoredToolResult struct {
	TurnID     string
	CallID     string
	Name       string
	ArgsHash   string
	ResultJSON string
	IsError    bool
	Handle     string
	DurationMS int64
}

// SnapshotSink 接收每步的上下文快照，由 session 注入实现。
type SnapshotSink interface {
	// SaveSnapshot 持久化一次组装结果。
	SaveSnapshot(ctx context.Context, turnID string, step int, model string, out *contextx.Output) error
}

// ProviderChoice 是降级链上的一个候选。
type ProviderChoice struct {
	Provider llm.Provider
	Model    string
}

// ReportResult 上报一次调用结果，供 session 更新 Provider 健康度。
type ReportResult func(providerName string, err *llm.Error)
