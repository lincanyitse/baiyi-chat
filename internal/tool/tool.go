// Package tool 定义工具协议：统一的 Spec、Tool 接口、权限分级与执行上下文。
//
// 设计约束（架构纲要 §9）：
//   - 本包不 import agent：工具需要的运行时信息通过 ExecContext 显式传入
//   - 工具失败一律转成 ToolResult(is_error=true) 交给模型自愈，不终止整轮
//   - 结果按 token 与字节双上限截断，完整内容落 output_store 并返回句柄
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// RiskLevel 是工具的风险等级，决定是否需要人工审批。
type RiskLevel int

const (
	// RiskRead 是只读操作，自动执行。
	RiskRead RiskLevel = iota
	// RiskWrite 会改变状态（写文件、发消息），牢笼内自动、越界需确认。
	RiskWrite
	// RiskDangerous 是 shell、删除、外发等不可逆操作，永远需要确认。
	RiskDangerous
)

func (r RiskLevel) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskDangerous:
		return "dangerous"
	default:
		return "unknown"
	}
}

// Spec 是工具的静态声明。
type Spec struct {
	// Name 面向模型，必须满足 ^[a-zA-Z0-9_-]{1,64}$。
	Name string
	// Description 必须写清「什么时候该用」与「什么时候不该用」。
	Description string
	// InputSchema 是 JSON Schema，要求 additionalProperties:false。
	InputSchema json.RawMessage
	// Risk 决定审批策略。
	Risk RiskLevel
	// Timeout 是单次执行上限，0 表示用默认值。
	Timeout time.Duration
}

// Tool 是所有工具的接口。内置工具、MCP 工具、HTTP 工具都实现它。
type Tool interface {
	Spec() Spec
	Execute(ctx context.Context, in json.RawMessage, ec *ExecContext) (Result, error)
}

// Result 是工具执行的返回值。
type Result struct {
	// Blocks 通常是单个 TextBlock，也可含 ImageBlock。
	Blocks []llm.Block
	// IsError 表示这是错误结果（仍然回填给模型，让它自己修正）。
	IsError bool
}

// TextResult 构造一个纯文本成功结果。
func TextResult(text string) Result {
	return Result{Blocks: []llm.Block{llm.NewTextBlock(text)}}
}

// ErrorResult 构造一个文本错误结果。
func ErrorResult(msg string) Result {
	return Result{
		Blocks:  []llm.Block{llm.NewTextBlock(msg)},
		IsError: true,
	}
}

// Text 提取结果中的全部文本，用于日志与回填。
func (r Result) Text() string { return llm.TextOfBlocks(r.Blocks) }

// TaintState 记录本轮是否消费过外部不可信内容。
//
// 一旦置位，写与危险级工具必须走审批（架构纲要 §13.3 污点追踪）。
type TaintState struct {
	external bool
	sources  []string
}

// MarkExternal 标记本轮消费了外部内容。
func (t *TaintState) MarkExternal(source string) {
	if t == nil {
		return
	}
	t.external = true
	if source != "" && len(t.sources) < 16 {
		t.sources = append(t.sources, source)
	}
}

// IsTainted 表示本轮已接触外部内容。
func (t *TaintState) IsTainted() bool { return t != nil && t.external }

// Sources 返回污染来源（用于审批提示）。
// ExecContext 是工具执行时的运行时环境。
type ExecContext struct {
	SessionID string
	TurnID    string
	CallID    string
	// WorkDir 是路径牢笼根，工具不得越界。
	WorkDir string
	// Approver 用于请求人工确认；nil 表示无人值守，危险操作应直接拒绝。
	Approver Approver
	// Taint 记录外部内容污染状态。
	Taint *TaintState
	// Outputs 用于大结果落盘。
	Outputs OutputStore
	// HTTP 是已注入 SSRF 防护的客户端。
	HTTP *http.Client
	// Clock 可注入，便于测试 time_now。
	Clock func() time.Time
	// Log 是系统日志器。
	Log *slog.Logger
}

// Now 返回当前时间，Clock 为 nil 时用系统时钟。
func (ec *ExecContext) Now() time.Time {
	if ec != nil && ec.Clock != nil {
		return ec.Clock()
	}
	return time.Now()
}

// ApprovalAction 是审批结果。
type ApprovalAction string

const (
	ApproveOnce    ApprovalAction = "once"
	ApproveSession ApprovalAction = "session"
	ApproveForever ApprovalAction = "forever" // 仅 RiskWrite 可用
	Deny           ApprovalAction = "deny"
)

// ApprovalRequest 是一次审批请求，可能包含同批的多个工具调用。
type ApprovalRequest struct {
	TurnID    string
	SessionID string
	Calls     []ApprovalCall
	ExpiresAt time.Time
}

// ApprovalCall 是待确认的单个工具调用。
type ApprovalCall struct {
	CallID  string
	Name    string
	Args    json.RawMessage
	Risk    RiskLevel
	Summary string
}

// ApprovalDecision 是审批结果。
type ApprovalDecision struct {
	Action ApprovalAction
	Reason string
}

// Approved 表示是否放行。
func (d ApprovalDecision) Approved() bool {
	return d.Action != Deny && d.Action != ""
}

// Approver 抽象审批交互：REPL 用终端 y/n，Web 用弹窗，Bot 用按钮。
type Approver interface {
	Request(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

// OutputStore 保存被截断的完整工具输出，返回可再用 tool_output_read 取回的句柄。
type OutputStore interface {
	// Save 保存内容并返回句柄。相同内容应返回相同句柄（内容寻址）。
	Save(ctx context.Context, turnID, callID, content string) (handle string, err error)
	// Load 读取句柄内容，offset 为字节偏移，limit 为最大返回字节数。
	Load(ctx context.Context, handle string, offset, limit int) (content string, total int, err error)
}

// DefaultSpec 填充默认值，供各工具复用。
func DefaultSpec(s Spec) Spec {
	if s.Timeout <= 0 {
		s.Timeout = 60 * time.Second
	}
	if len(s.InputSchema) == 0 {
		s.InputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	return s
}

// ValidateSpec 检查工具名是否符合约束。
func ValidateSpec(s Spec) error {
	if s.Name == "" {
		return fmt.Errorf("工具名为空")
	}
	if len(s.Name) > 64 {
		return fmt.Errorf("工具名 %q 超过 64 字符", s.Name)
	}
	for _, r := range s.Name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("工具名 %q 含非法字符 %q", s.Name, r)
		}
	}
	if len(s.InputSchema) > 0 && !json.Valid(s.InputSchema) {
		return fmt.Errorf("工具 %q 的 InputSchema 不是合法 JSON", s.Name)
	}
	return nil
}
