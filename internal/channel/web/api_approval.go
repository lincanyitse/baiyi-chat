package web

import (
	"net/http"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web/stream"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// handlePendingApprovals 返回当前挂起的审批。
//
// 页面刷新后弹窗要能重新出现，靠的就是这个接口。
func (s *Server) handlePendingApprovals(w http.ResponseWriter, r *http.Request) {
	if s.opts.Approvals == nil {
		writeJSON(w, http.StatusOK, []ApprovalRequest{})
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Approvals.Pending())
}

// handleApprovalDecision 提交用户对一条审批的决定。
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	if s.opts.Approvals == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "本项目未启用审批")
		return
	}

	id := r.PathValue("id")
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	action := tool.ApprovalAction(in.Action)
	switch action {
	case tool.ApproveOnce, tool.ApproveSession, tool.ApproveForever, tool.Deny:
	default:
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "未知的审批动作: "+in.Action)
		return
	}

	if !s.opts.Approvals.Decide(id, tool.ApprovalDecision{Action: action, Reason: in.Reason}) {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "审批不存在或已处理")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "action": action})
}

// handleApprovalStream 以 SSE 推送新出现的审批。
//
// 只在「有新审批」时发一条事件，前端收到后拉一次 /pending 拿全量。
// 不做增量同步：待审批数量极少，拉全量最省心。
func (s *Server) handleApprovalStream(w http.ResponseWriter, r *http.Request) {
	if s.opts.Approvals == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "本项目未启用审批")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "当前服务器不支持流式响应")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	cancel := r.Context().Done()
	// 先推一次，让刚打开的页面立刻拿到已有审批
	if pending := s.opts.Approvals.Pending(); len(pending) > 0 {
		stream.WriteEvent(w, "approvals", pending)
		flusher.Flush()
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		notify := s.opts.Approvals.Notify()
		select {
		case <-notify:
			// 有变化（新增或解决）：推一次全量，前端据此增删弹窗
			stream.WriteEvent(w, "approvals", s.opts.Approvals.Pending())
			flusher.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-cancel:
			return
		}
	}
}
