package web

import (
	"net/http"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web/stream"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// handleRegenerate 对一条助手消息重新生成回答，产生新分支。
//
// 与 handleStartTurn 一样「先建后跑」：立刻返回 turn_id，前端订阅
// /api/turns/{id}/events 看流式输出。
func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	msgID := r.PathValue("id")

	// 先查消息，用它拿到会话——URL 里只有 message id
	msg, err := s.opts.Service.GetMessage(r.Context(), msgID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	turn, plan, err := s.opts.Service.Regenerate(r.Context(), msg.SessionID, session.RegenerateOptions{
		MessageID: msgID,
	})
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	log := s.opts.Streams.Ensure(turn.ID)
	go func() {
		res, err := s.opts.Service.RunBranchTurn(
			s.opts.BaseContext, msg.SessionID, plan, turn.ID,
			s.streamCallback(turn.ID, log),
			s.askOptions(),
		)
		log.Close(buildFinal(turn.ID, res, err))
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"turn_id":    turn.ID,
		"session_id": msg.SessionID,
	})
}

// handleEditMessage 编辑一条用户消息，产生新分支。
func (s *Server) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	msgID := r.PathValue("id")

	var in struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Text == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "text 不能为空")
		return
	}

	msg, err := s.opts.Service.GetMessage(r.Context(), msgID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	turn, plan, err := s.opts.Service.EditMessage(r.Context(), msg.SessionID, session.EditOptions{
		MessageID: msgID,
		Text:      in.Text,
	})
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	log := s.opts.Streams.Ensure(turn.ID)
	go func() {
		res, err := s.opts.Service.RunBranchTurn(
			s.opts.BaseContext, msg.SessionID, plan, turn.ID,
			s.streamCallback(turn.ID, log),
			s.askOptions(),
		)
		log.Close(buildFinal(turn.ID, res, err))
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"turn_id":    turn.ID,
		"session_id": msg.SessionID,
	})
}

// handleSiblings 返回一条消息的全部兄弟，供分支切换器使用。
//
// 每个兄弟附带 leaf_id：切过去之后应该把哪条消息作为视图末端。
func (s *Server) handleSiblings(w http.ResponseWriter, r *http.Request) {
	msgID := r.PathValue("id")

	msg, err := s.opts.Service.GetMessage(r.Context(), msgID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	opts, err := s.opts.Service.BranchOptions(r.Context(), msg.SessionID, msgID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	type siblingDTO struct {
		MessageDTO
		LeafID string `json:"leaf_id"`
	}
	out := make([]siblingDTO, 0, len(opts))
	for _, o := range opts {
		out = append(out, siblingDTO{MessageDTO: toMessageDTO(o.Message), LeafID: o.Leaf})
	}
	writeJSON(w, http.StatusOK, out)
}

// streamCallback 构造统一的 SSE 回调。
func (s *Server) streamCallback(turnID string, log *stream.Log) session.StreamCallback {
	return func(ev llm.Event) {
		kind, data, ok := encodeEvent(turnID, ev)
		if !ok {
			return
		}
		log.Append(kind, data)
	}
}
