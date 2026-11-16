package web

import (
	"net/http"
	"strconv"

	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// SegmentDTO 是上下文某一段的对外表示。
type SegmentDTO struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
	// Quota 是该段的预算上限；0 表示不限（如身份段）。
	Quota  int  `json:"quota"`
	Frozen bool `json:"frozen"`
	// Trims 是这一段的裁剪记录，说明「为什么少了」。
	Trims []string `json:"trims,omitempty"`
}

// ContextSnapshotDTO 是一次上下文快照的对外表示。
type ContextSnapshotDTO struct {
	ID        string       `json:"id"`
	TurnID    string       `json:"turn_id"`
	Step      int          `json:"step"`
	Model     string       `json:"model"`
	Segments  []SegmentDTO `json:"segments"`
	Total     int          `json:"total_tokens"`
	Available int          `json:"available_tokens"`
	CreatedAt int64        `json:"created_at"`
	PrevID    string       `json:"prev_id,omitempty"`
}

// ContextSnapshotMetaDTO 是列表用的轻量信息。
type ContextSnapshotMetaDTO struct {
	ID        string `json:"id"`
	TurnID    string `json:"turn_id"`
	Step      int    `json:"step"`
	Model     string `json:"model"`
	Total     int    `json:"total_tokens"`
	CreatedAt int64  `json:"created_at"`
}

// toSnapshotDTO 把内部快照转成对外表示。
func toSnapshotDTO(s session.ContextSnapshot) ContextSnapshotDTO {
	segs := make([]SegmentDTO, 0, len(s.Segments))
	for _, seg := range s.Segments {
		kind := string(seg.Kind)
		dto := SegmentDTO{
			Kind:   kind,
			Label:  seg.Kind.Label(),
			Tokens: seg.Tokens,
			Quota:  s.Budget.Allocated[kind],
			Frozen: seg.Frozen,
		}
		for _, t := range seg.Trims {
			dto.Trims = append(dto.Trims, t.Reason.Label())
		}
		segs = append(segs, dto)
	}

	return ContextSnapshotDTO{
		ID:        s.ID,
		TurnID:    s.TurnID,
		Step:      s.Step,
		Model:     s.Model,
		Segments:  segs,
		Total:     s.Total,
		Available: s.Available,
		CreatedAt: s.CreatedAt.UnixMilli(),
		PrevID:    s.PrevID,
	}
}

// handleSessionContexts 返回会话最近的快照列表。
func (s *Server) handleSessionContexts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.opts.Service.GetSession(r.Context(), id); err != nil {
		writeSessionErr(w, err)
		return
	}

	list, err := s.opts.Service.ListContextSnapshots(r.Context(), id, queryInt(r, "limit", 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	out := make([]ContextSnapshotMetaDTO, 0, len(list))
	for _, m := range list {
		out = append(out, ContextSnapshotMetaDTO{
			ID: m.ID, TurnID: m.TurnID, Step: m.Step,
			Model: m.Model, Total: m.Total, CreatedAt: m.CreatedAt.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTurnContext 返回某轮次的完整快照。
func (s *Server) handleTurnContext(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	step := -1
	if v := r.URL.Query().Get("step"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "step 必须是非负整数")
			return
		}
		step = n
	}

	snap, err := s.opts.Service.ContextSnapshotAt(r.Context(), turnID, step)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSnapshotDTO(snap))
}

// ContextDiffDTO 是一轮与上一轮的逐段对比。
type ContextDiffDTO struct {
	TurnID string `json:"turn_id"`
	// HasPrev 为假表示这是首轮，没有可对比对象。
	HasPrev  bool                  `json:"has_prev"`
	Segments []session.SegmentDiff `json:"segments"`
}

// handleTurnContextDiff 返回该轮与上一轮的逐段差异。
func (s *Server) handleTurnContextDiff(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	cur, err := s.opts.Service.ContextSnapshotAt(r.Context(), turnID, -1)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	out := ContextDiffDTO{TurnID: turnID}
	if cur.PrevID == "" {
		// 首轮：全标 added，让前端能画出「从无到有」
		out.Segments = session.DiffContextSnapshots(session.ContextSnapshot{}, cur)
		writeJSON(w, http.StatusOK, out)
		return
	}

	prev, err := s.opts.Service.ContextSnapshotByID(r.Context(), cur.PrevID)
	if err != nil {
		// 上一轮查不到不算错误：可能是被清理了，退化成「全部新增」
		out.Segments = session.DiffContextSnapshots(session.ContextSnapshot{}, cur)
		writeJSON(w, http.StatusOK, out)
		return
	}

	out.HasPrev = true
	out.Segments = session.DiffContextSnapshots(prev, cur)
	writeJSON(w, http.StatusOK, out)
}
