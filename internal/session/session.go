// Package session 管理会话、消息与轮次的持久化。
//
// transcript 是 append-only 的：编辑与重生成产生新分支（parent_id），不修改历史。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// ErrNotFound 表示会话、消息或轮次不存在。
//
// 用哨兵错误而不是让渠道层匹配错误文本：文案是给人看的，随时可能
// 调整；HTTP 状态码是协议的一部分，不该跟着文案一起变。
var ErrNotFound = errors.New("not found")

// ErrInvalid 表示请求参数在业务上不成立（角色不对、内容为空等）。
var ErrInvalid = errors.New("invalid argument")

// Session 是一个会话。
type Session struct {
	ID         string
	Title      string
	PresetID   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
	// ModelRef 是会话级模型覆盖，格式 "provider/model"。
	// 空串表示跟随任务路由（route.chat 的候选链）。
	ModelRef string
}

// TurnStatus 是轮次状态。
type TurnStatus string

const (
	TurnReceived TurnStatus = "received"
	TurnRunning  TurnStatus = "running"
	TurnAwaiting TurnStatus = "awaiting"
	TurnCanceled TurnStatus = "canceled"
	TurnFailed   TurnStatus = "failed"
	TurnDone     TurnStatus = "done"
)

// SessionQuery 是会话列表的筛选条件。
type SessionQuery struct {
	// Query 按标题做不区分大小写的子串匹配。空表示不过滤。
	Query string
	// IncludeArchived 为 true 时连已归档的一起返回。
	IncludeArchived bool
	// Limit 是本页上限；<=0 时用默认值。
	Limit int
	// Offset 跳过前多少条，用于翻页。
	Offset int
}

// SessionPage 是一页会话与总数。
//
// Total 让界面能显示「共 N 个」并判断还有没有下一页——
// 只回一页数据的话，前端永远不知道是不是到底了。
type SessionPage struct {
	Items []Session
	Total int
}

// Turn 是一次用户输入到最终答复的过程。
type Turn struct {
	ID        string
	SessionID string
	Status    TurnStatus
	Error     string
	StartedAt time.Time
	EndedAt   *time.Time
}

// StoredMessage 是落库后的消息。
type StoredMessage struct {
	ID        string
	SessionID string
	Seq       int
	ParentID  string
	Role      llm.Role
	Content   []llm.Block
	Trust     llm.Trust
	Pinned    bool
	Compacted bool
	CreatedAt time.Time
}

// Repository 是会话持久化接口，由 store/sqlite 实现。
type Repository interface {
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context, limit int) ([]Session, error)
	// SearchSessions 按标题关键词与归档状态筛选，支持分页。
	// query 为空表示不过滤；includeArchived 为 false 时只返回未归档的。
	SearchSessions(ctx context.Context, opts SessionQuery) (SessionPage, error)
	// SearchMessages 在单个会话的消息正文里搜关键词。
	SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]StoredMessage, error)
	UpdateSessionTitle(ctx context.Context, id, title string) error
	// UpdateSessionModel 设置会话级模型覆盖；空串表示跟随路由。
	UpdateSessionModel(ctx context.Context, id, modelRef string) error
	// ArchiveSession 归档会话；at 为 nil 表示取消归档。
	ArchiveSession(ctx context.Context, id string, at *time.Time) error
	// DeleteSession 软删除会话。
	DeleteSession(ctx context.Context, id string) error
	TouchSession(ctx context.Context, id string, at time.Time) error

	AppendMessage(ctx context.Context, m StoredMessage) error
	ListMessages(ctx context.Context, sessionID string) ([]StoredMessage, error)
	// GetMessage 按 ID 取单条消息，用于重新生成与编辑时定位父节点。
	GetMessage(ctx context.Context, id string) (StoredMessage, error)
	NextSeq(ctx context.Context, sessionID string) (int, error)

	CreateTurn(ctx context.Context, t Turn) error
	UpdateTurnStatus(ctx context.Context, id string, status TurnStatus, errMsg string, endedAt *time.Time) error
	GetTurn(ctx context.Context, id string) (Turn, error)
	ListTurns(ctx context.Context, sessionID string) ([]Turn, error)
	// FindResumable 返回给定会话中处于 running/awaiting 状态的最近一个轮次。
	FindResumable(ctx context.Context, sessionID string) (Turn, bool, error)

	// LoadSummary 读取会话摘要状态。
	LoadSummary(ctx context.Context, sessionID string) (contextx.SummaryState, bool, error)
	// SaveSummary 写入会话摘要状态。
	SaveSummary(ctx context.Context, st contextx.SummaryState) error

	// SaveSnapshot 写入一条上下文快照，并自动把同会话上一条的 ID
	// 记进 prev_snapshot_id。
	SaveSnapshot(ctx context.Context, s ContextSnapshotInsert) error
	// GetContextSnapshot 取某轮次指定步的快照。step < 0 表示该轮最后一步。
	// 不存在时返回 ErrNotFound。
	GetContextSnapshot(ctx context.Context, turnID string, step int) (ContextSnapshot, error)
	// GetContextSnapshotByID 按主键取快照。不存在时返回 ErrNotFound。
	GetContextSnapshotByID(ctx context.Context, id string) (ContextSnapshot, error)
	// ListContextSnapshots 返回某会话最近的快照元信息，按时间倒序。
	// limit <= 0 时由实现选用默认上限。
	ListContextSnapshots(ctx context.Context, sessionID string, limit int) ([]ContextSnapshotMeta, error)

	// GetToolResult 查询工具调用的幂等缓存。不存在时 ok 为 false 且 err 为 nil。
	GetToolResult(ctx context.Context, turnID, callID string) (agent.StoredToolResult, bool, error)
	// SaveToolResult 写入幂等缓存。同一 (turn, call) 已存在时忽略。
	SaveToolResult(ctx context.Context, r agent.StoredToolResult) error
}

// MarshalBlocks 把内容块序列化为 JSON。
func MarshalBlocks(blocks []llm.Block) (string, error) {
	b, err := json.Marshal(blocks)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalBlocks 反序列化内容块。每个块带 type 字段，据此还原具体类型。
func UnmarshalBlocks(data string) ([]llm.Block, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, err
	}
	out := make([]llm.Block, 0, len(raw))
	for _, item := range raw {
		var head struct {
			Type llm.BlockType `json:"type"`
		}
		if err := json.Unmarshal(item, &head); err != nil {
			return nil, err
		}
		blk, err := decodeBlock(head.Type, item)
		if err != nil {
			return nil, err
		}
		out = append(out, blk)
	}
	return out, nil
}

func decodeBlock(t llm.BlockType, raw json.RawMessage) (llm.Block, error) {
	switch t {
	case llm.BlockText:
		var b llm.TextBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockImage:
		var b llm.ImageBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockFile:
		var b llm.FileBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockToolUse:
		var b llm.ToolUseBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockToolResult:
		// Content 是 []llm.Block 接口，encoding/json 无法直接还原，
		// 先解码为中间结构再递归还原。
		var mid struct {
			Type      string            `json:"type"`
			ToolUseID string            `json:"tool_use_id"`
			Content   []json.RawMessage `json:"content"`
			IsError   bool              `json:"is_error,omitempty"`
			Truncated bool              `json:"truncated,omitempty"`
			Handle    string            `json:"handle,omitempty"`
		}
		if err := json.Unmarshal(raw, &mid); err != nil {
			return nil, err
		}
		inner, err := UnmarshalBlocks(mustJSON(mid.Content))
		if err != nil {
			return nil, err
		}
		return llm.ToolResultBlock{
			Type:      string(llm.BlockToolResult),
			ToolUseID: mid.ToolUseID,
			Content:   inner,
			IsError:   mid.IsError,
			Truncated: mid.Truncated,
			Handle:    mid.Handle,
		}, nil
	case llm.BlockThinking:
		var b llm.ThinkingBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockCitation:
		var b llm.CitationBlock
		return b, json.Unmarshal(raw, &b)
	case llm.BlockAudio:
		var b llm.AudioBlock
		return b, json.Unmarshal(raw, &b)
	default:
		return nil, fmt.Errorf("未知的内容块类型: %q", t)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}
