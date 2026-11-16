package web

import (
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// SessionDTO 是会话的对外表示。
//
// 与 session.Session 分开：内部类型加字段时不该意外泄漏到 API，
// time.Time 的默认 JSON 编码也不适合前端直接消费。
type SessionDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"` // Unix 毫秒
	UpdatedAt int64  `json:"updated_at"`
	Archived  bool   `json:"archived"`
	// ModelRef 是会话级模型覆盖，空串表示跟随路由默认。
	ModelRef string `json:"model_ref"`
}

func toSessionDTO(s session.Session) SessionDTO {
	return SessionDTO{
		ID:        s.ID,
		Title:     s.Title,
		CreatedAt: s.CreatedAt.UnixMilli(),
		UpdatedAt: s.UpdatedAt.UnixMilli(),
		Archived:  s.ArchivedAt != nil,
		ModelRef:  s.ModelRef,
	}
}

func toSessionDTOs(list []session.Session) []SessionDTO {
	out := make([]SessionDTO, len(list))
	for i, s := range list {
		out[i] = toSessionDTO(s)
	}
	return out
}

// MessageNodeDTO 是消息树上的一个节点。
type MessageNodeDTO struct {
	MessageDTO
	// Siblings 是同父节点下的消息总数，Index 是其中的序号（从 1 开始）。
	// 只有 Siblings > 1 时前端才显示「← 2/3 →」切换器。
	Siblings int `json:"siblings"`
	Index    int `json:"index"`
	// Children 是它下面可分叉的子节点数。
	Children int `json:"children"`
}

// MessageTreeDTO 是一个会话的完整分支视图。
type MessageTreeDTO struct {
	Leaf     string           `json:"leaf"`
	Messages []MessageNodeDTO `json:"messages"`
}

// ErrorBody 是统一的错误响应。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse 包装错误体。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// 错误码。前端据此决定提示文案与是否重试。
const (
	ErrCodeBadRequest   = "bad_request"
	ErrCodeNotFound     = "not_found"
	ErrCodeConflict     = "conflict"
	ErrCodeUnauthorized = "unauthorized"
	ErrCodeInternal     = "internal"
	ErrCodeUnavailable  = "unavailable"
)

// HealthDTO 是健康检查的响应。
type HealthDTO struct {
	Status    string `json:"status"`
	Version   string `json:"version,omitempty"`
	UptimeSec int64  `json:"uptime_sec"`
	// StreamBuffers 暴露 SSE 缓冲占用，便于观察内存是否失控。
	StreamTurns   int `json:"stream_turns"`
	StreamEntries int `json:"stream_entries"`
	StreamBytes   int `json:"stream_bytes"`
}

// unixMilli 是辅助函数，把可选时间转成毫秒。
func unixMilli(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}
