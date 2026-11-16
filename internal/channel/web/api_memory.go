package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/memory"
)

// MemoryAdmin 是记忆管理界面需要的能力，由 memory.Admin 实现。
//
// 定义在消费方：Web 只声明自己调用的方法，不依赖 session.Service 上的那一层转发。
type MemoryAdmin interface {
	ListFacts(ctx context.Context, status string, limit int) ([]memory.FactView, error)
	ApplyFactAction(ctx context.Context, id string, action memory.FactAction) error
	EditFact(ctx context.Context, id, newValue string) error
	FactEvents(ctx context.Context, id string) ([]memory.MemoryEventView, error)
	ProfileCardReadOnly(ctx context.Context) (string, error)
	Stats(ctx context.Context) (memory.MemoryStatsView, error)
}

// 编译期确认 memory.Admin 满足 Web 需要的方法。
var _ MemoryAdmin = (*memory.Admin)(nil)

func (s *Server) memoryAdmin() (MemoryAdmin, error) {
	if s.opts.Memory == nil {
		return nil, fmt.Errorf("记忆子系统未启用")
	}
	return s.opts.Memory, nil
}

// handleListFacts 返回记忆事实列表。
func (s *Server) handleListFacts(w http.ResponseWriter, r *http.Request) {
	mem, err := s.memoryAdmin()
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	limit := queryInt(r, "limit", 100)

	facts, err := mem.ListFacts(r.Context(), status, limit)
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, facts)
}

// handleFactAction 对一条事实执行确认/标疑/删除/恢复，或改值。
func (s *Server) handleFactAction(w http.ResponseWriter, r *http.Request) {
	mem, err := s.memoryAdmin()
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	id := r.PathValue("id")

	var in struct {
		Action string `json:"action"`
		// Value 只在 action=edit 时使用。
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	if in.Action == string(memory.FactEdit) {
		if err := mem.EditFact(r.Context(), id, in.Value); err != nil {
			writeMemoryErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "action": in.Action})
		return
	}

	if err := mem.ApplyFactAction(r.Context(), id, memory.FactAction(in.Action)); err != nil {
		writeMemoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "action": in.Action})
}

// handleFactEvents 返回一条事实的审计链。
func (s *Server) handleFactEvents(w http.ResponseWriter, r *http.Request) {
	mem, err := s.memoryAdmin()
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	events, err := mem.FactEvents(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// handleProfileCard 返回画像卡（只读，不累加访问计数）。
func (s *Server) handleProfileCard(w http.ResponseWriter, r *http.Request) {
	mem, err := s.memoryAdmin()
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	card, err := mem.ProfileCardReadOnly(r.Context())
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"card": card})
}

// handleMemoryStats 返回记忆子系统计数。
func (s *Server) handleMemoryStats(w http.ResponseWriter, r *http.Request) {
	mem, err := s.memoryAdmin()
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	st, err := mem.Stats(r.Context())
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"active_facts": st.ActiveFacts,
		"disputed":     st.Disputed,
		"episodes":     st.Episodes,
		"archived_eps": st.ArchivedEps,
		"candidates":   st.Candidates,
	})
}

// writeMemoryErr 映射记忆相关错误。
func writeMemoryErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "未启用"), strings.Contains(msg, "不存在"):
		writeError(w, http.StatusNotFound, ErrCodeNotFound, msg)
	default:
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, msg)
	}
}
