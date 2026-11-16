package web

import (
	"net/http"
)

// ResetScope 是重置范围。
type ResetScope string

const (
	// ResetSessions 只清对话：会话、消息、轮次、调用记录、上下文快照。
	ResetSessions ResetScope = "sessions"
	// ResetKnowledge 清记忆与知识库：事实、情节、文档、向量。
	ResetKnowledge ResetScope = "knowledge"
	// ResetAll 以上全清（仍保留口令、密钥与价格）。
	ResetAll ResetScope = "all"
)

// handleResetData 清空指定范围的数据。
//
// 这是不可逆操作。范围限定得很死：无论选哪一档，都不碰口令、
// 加密密钥与价格——清掉它们等于把用户锁在门外、或让他配好的
// 模型失效。
func (s *Server) handleResetData(w http.ResponseWriter, r *http.Request) {
	if s.opts.ResetData == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "重置不可用")
		return
	}

	var in struct {
		Scope string `json:"scope"`
		// Confirm 必须与 scope 完全一致，防止误点。
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	scope := ResetScope(in.Scope)
	switch scope {
	case ResetSessions, ResetKnowledge, ResetAll:
	default:
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "未知的重置范围: "+in.Scope)
		return
	}

	if in.Confirm != in.Scope {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
			"确认文本不匹配。要删除数据，请把确认框填成 "+in.Scope)
		return
	}

	summary, err := s.opts.ResetData(r.Context(), string(scope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	s.opts.Log.Warn("用户清空了数据", "scope", string(scope), "summary", summary)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"scope":   string(scope),
		"summary": summary,
	})
}
