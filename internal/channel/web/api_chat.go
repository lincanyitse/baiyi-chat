package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web/stream"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// MessageDTO 是一条消息的对外表示。
type MessageDTO struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	Content   []BlockDTO `json:"content"`
	CreatedAt int64      `json:"created_at"`
}

// BlockDTO 是内容块的对外表示。
//
// 引用相关字段平时为空，只在 CitationBlock 上有值。合在一个结构里
// 而不是另建类型，是为了让前端按 content[].type 统一处理。
type BlockDTO struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`

	// RAG 引用
	Index       int    `json:"index,omitempty"`
	DocID       string `json:"doc_id,omitempty"`
	ChunkID     string `json:"chunk_id,omitempty"`
	Start       int    `json:"start,omitempty"`
	End         int    `json:"end,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadingPath string `json:"heading_path,omitempty"`
	Page        int    `json:"page,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	URI         string `json:"uri,omitempty"`
}

func toMessageDTO(m llm.Message) MessageDTO {
	blocks := make([]BlockDTO, 0, len(m.Content))
	for _, b := range m.Content {
		switch v := b.(type) {
		case llm.TextBlock:
			blocks = append(blocks, BlockDTO{Type: string(llm.BlockText), Text: v.Text})
		case llm.ThinkingBlock:
			blocks = append(blocks, BlockDTO{Type: string(llm.BlockThinking), Text: v.Text})
		case llm.ToolUseBlock:
			blocks = append(blocks, BlockDTO{
				Type: string(llm.BlockToolUse), ID: v.ID, Name: v.Name, Input: v.Input,
			})
		case llm.ToolResultBlock:
			blocks = append(blocks, BlockDTO{
				Type:    string(llm.BlockToolResult),
				ID:      v.ToolUseID,
				Text:    llm.TextOfBlocks(v.Content),
				IsError: v.IsError,
			})
		case llm.CitationBlock:
			// 之前漏了这个 case，引用块被静默丢弃，回答里的 [1]
			// 变成死文字。doc_id + chunk_id + start/end 足以让前端
			// 跳到文档片段的确切位置。
			blocks = append(blocks, BlockDTO{
				Type:        string(llm.BlockCitation),
				Index:       v.Index,
				DocID:       v.DocID,
				ChunkID:     v.ChunkID,
				Start:       v.Start,
				End:         v.End,
				Title:       v.Title,
				HeadingPath: v.HeadingPath,
				Page:        v.Page,
				Snippet:     v.Snippet,
				URI:         v.URI,
			})
		}
	}
	return MessageDTO{
		ID:        m.ID,
		Role:      string(m.Role),
		Content:   blocks,
		CreatedAt: m.CreatedAt.UnixMilli(),
	}
}

// handleStartTurn 发起一轮对话，返回 turn_id。
//
// 立即返回而不是在响应里流式输出：停止按钮需要一个与控制通道解耦的
// turn 标识，断线重连需要同一轮次能被第二个连接订阅，关闭标签页不该
// 中断生成。代价是一轮两条请求。
func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	var in struct {
		Text string `json:"text"`
		// AttachmentIDs 是先前通过 /api/attachments 上传的附件。
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Text == "" && len(in.AttachmentIDs) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "text 与附件不能同时为空")
		return
	}
	if len(in.AttachmentIDs) > maxAttachmentsTurn {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("单轮最多 %d 个附件", maxAttachmentsTurn))
		return
	}

	// 先确认会话存在，避免为一个不存在的会话创建轮次
	sess, err := s.opts.Service.GetSession(r.Context(), sessionID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	// 会话指定的模型不可用时当场拒绝：让用户在提交时看到错误，
	// 而不是等订阅了 SSE 才发现这一轮根本没开始。
	if err := s.opts.Service.ValidateSessionModel(sess); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	// 附件要先加载成内容块，再检查当前模型能不能看图
	attachBlocks, err := s.loadAttachments(r.Context(), in.AttachmentIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	if err := s.checkVisionSupport(sess, attachBlocks); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	turn, err := s.opts.Service.PrepareTurn(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	log := s.opts.Streams.Ensure(turn.ID)
	go s.runTurnWith(sessionID, turn.ID, in.Text, attachBlocks, log)

	writeJSON(w, http.StatusAccepted, map[string]string{"turn_id": turn.ID})
}

// runTurnWith 跑完一轮，把事件写进日志。
//
// 用独立 ctx（不随 HTTP 请求取消）：关闭标签页不该中断生成，
// 用户可能是误关，重新打开还等着看结果。停止只能通过显式接口。
func (s *Server) runTurnWith(sessionID, turnID, text string, attachments []llm.Block, log *stream.Log) {
	opts := s.askOptions()
	opts.Attachments = attachments

	res, err := s.opts.Service.AskTurn(s.opts.BaseContext, sessionID, turnID, text,
		s.streamCallback(turnID, log), opts)
	log.Close(buildFinal(turnID, res, err))
}

// loadAttachments 把附件 ID 读成内容块。
//
// 图片 → ImageBlock（Provider 会转成 base64 或 URL）；
// 文档 → 同时进 RAG 并附 FileBlock；
// 其余 → FileBlock 透传。
// loadAttachments 把附件 ID 读成内容块。
//
// 图片 → ImageBlock（Provider 会转成 base64 或 URL）；
// 文档 → 同时进 RAG 并附 FileBlock；
// 其余 → FileBlock 透传。
//
// ctx 是请求 ctx：解析与切分是同步做的，用户取消或服务关停时
// 不该继续跑完一个 8 MiB 文档。
func (s *Server) loadAttachments(ctx context.Context, ids []string) ([]llm.Block, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.opts.Attachments == nil {
		return nil, fmt.Errorf("附件功能未启用")
	}

	blocks := make([]llm.Block, 0, len(ids))
	for _, id := range ids {
		data, info, err := s.opts.Attachments.Load(id)
		if err != nil {
			return nil, fmt.Errorf("附件 %s 不可用: %w", id, err)
		}

		switch classify(info.MediaType, info.Name) {
		case "image":
			blocks = append(blocks, llm.NewImageBlock(llm.ImageSource{
				Data: data, MediaType: info.MediaType,
			}, ""))
		case "document":
			// 文档同时进知识库，之后能被 rag_search 检索到
			if s.ragAvailable() && s.opts.RAG.Ingester != nil {
				if _, err := s.opts.RAG.Ingester.IngestBytes(
					ctx, info.Name, info.MediaType, data); err != nil {
					s.opts.Log.Warn("附件入库失败，仍作为文件块发送", "name", info.Name, "err", err)
				}
			}
			blocks = append(blocks, llm.FileBlock{
				Type: string(llm.BlockFile), Name: info.Name, MediaType: info.MediaType, Data: data,
			})
		default:
			blocks = append(blocks, llm.FileBlock{
				Type: string(llm.BlockFile), Name: info.Name, MediaType: info.MediaType, Data: data,
			})
		}
	}
	return blocks, nil
}

// checkVisionSupport 在带图时确认当前模型支持视觉。
//
// 这是全仓唯一一处消费 Model.Vision 的地方——此前它只被存储与回显。
//
// 解析不出模型时也拒绝：无法确认支持视觉就把图发出去，等于赌下游
// 会给出可读的错误。宁可在这里明说「无法确定」。
func (s *Server) checkVisionSupport(sess session.Session, blocks []llm.Block) error {
	hasImage := false
	for _, b := range blocks {
		if _, ok := b.(llm.ImageBlock); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return nil
	}

	ref, ok := s.opts.Service.SessionModel(sess)
	if !ok {
		return fmt.Errorf("无法确定当前模型是否支持视觉输入，请在上方明确选择模型")
	}
	caps := s.opts.Service.ModelCapabilities(ref)
	if caps.Vision {
		return nil
	}
	return fmt.Errorf("当前模型 %s 不支持视觉输入，请在上方切换支持视觉的模型", ref.Model)
}

// askOptions 构造带审批能力的选项。
//
// 三个入口（普通发送、重新生成、编辑）都必须用它：少了 SessionApproved，
// 「本会话允许」按钮就只是摆设，每次调用都会重新弹窗；少了 Approver，
// agent 会直接拒绝所有写类工具。
func (s *Server) askOptions() session.AskOptions {
	opts := session.AskOptions{Approver: s.opts.Approver}
	if s.opts.Approvals != nil {
		opts.SessionApproved = s.opts.Approvals.IsApproved
		opts.MarkApproved = s.opts.Approvals.MarkApproved
	}
	return opts
}

// encodeEvent 把 llm.Event 编码成 SSE 事件。
//
// 不直接 json.Marshal(ev)：Event 是接口，没有类型判别字段，
// 前端无法据此分派；而且 BlockStartEvent.Type 是块类型，会与事件
// 类型混淆。统一信封 `{kind, data}` 解决这两点。
func encodeEvent(turnID string, ev llm.Event) (string, []byte, bool) {
	// 逐字增量太大且重放意义不大，只保留最终完整事件。
	// 前端仍能看到「参数正在生成」以外的全部过程。
	if _, ok := ev.(llm.ToolArgsDeltaEvent); ok {
		return "", nil, false
	}

	kind := string(ev.EventType())
	data, err := json.Marshal(ev)
	if err != nil {
		return "", nil, false
	}
	return kind, data, true
}

// buildFinal 构造轮次结束时的最终事件载荷。
//
// 只带前端真正消费的字段：status 决定是否显示失败提示，error 是要
// 显示的文案，turn_id 供对账。用量与费用走独立的 usage 事件，
// 不在这里重复一遍。
func buildFinal(turnID string, res session.TurnResult, err error) json.RawMessage {
	status := "done"
	errMsg := ""
	if err != nil {
		if errors.Is(err, context.Canceled) {
			status = "canceled"
		} else {
			status = "failed"
			errMsg = humanizeError(err)
		}
	}

	payload := map[string]any{
		"turn_id": turnID,
		"status":  status,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// humanizeError 把底层错误翻成用户能照做的中文。
//
// 原始文案形如 `llm transient (http 0): Post "https://...": dial tcp: connectex...`，
// 对用户没有意义——他要的是「哪出了问题、我该做什么」。
// 技术细节仍完整写进日志，界面只展示能据以行动的那一句。
//
// 改一处而不是每个调用点各写一遍：这是界面与内核的唯一翻译边界。
func humanizeError(err error) string {
	e := llm.AsError(err)

	switch e.Kind {
	case llm.ErrTransient:
		return "连不上模型服务。检查网络，或确认网关地址是否正确。"
	case llm.ErrRateLimited:
		return "请求太频繁，被模型服务限流了。稍等几秒再试。"
	case llm.ErrAuth:
		return "认证失败。检查 API Key 是否有效、环境变量是否已设置。"
	case llm.ErrQuota:
		return "额度用完了。检查模型服务的余额或配额。"
	case llm.ErrInvalidRequest:
		return "模型服务拒绝了这次请求（参数不被接受）。详情见日志。"
	case llm.ErrContextOverflow:
		return "对话太长了，超出模型的上下文窗口。试试新建会话，或换一个上下文更大的模型。"
	case llm.ErrPolicy:
		return "内容被模型服务的安全策略拦截了。"
	case llm.ErrNotSupported:
		return "当前模型不支持这个操作（比如视觉输入或工具调用）。换一个模型试试。"
	case llm.ErrCanceled:
		return "已取消。"
	}

	// 未知分类：保留原文但去掉前缀噪音，让用户至少能搜到
	if e.Message != "" {
		return "出错了：" + e.Message
	}
	return "出错了：" + err.Error()
}

// handleTurnEvents 以 SSE 推送一轮的事件。
func (s *Server) handleTurnEvents(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	log := s.opts.Streams.Get(turnID)
	if log == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "轮次不存在或已过期")
		return
	}

	stream.Serve(w, r, log, stream.ParseLastEventID(r), s.opts.Log.Info)
}

// handleTurnStatus 返回轮次状态，供客户端在 SSE 不可用时对账。
func (s *Server) handleTurnStatus(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	turn, err := s.opts.Service.GetTurn(r.Context(), turnID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	status := map[string]any{
		"turn_id":    turn.ID,
		"session_id": turn.SessionID,
		"status":     string(turn.Status),
		"error":      turn.Error,
		"started_at": turn.StartedAt.UnixMilli(),
		"ended_at":   unixMilli(turn.EndedAt),
	}
	writeJSON(w, http.StatusOK, status)
}

// handleStopTurn 停止正在生成的一轮。
func (s *Server) handleStopTurn(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	if !s.opts.Service.IsTurnRunning(turnID) {
		// 已经结束：幂等返回成功，前端不必区分「停得太晚」与「停止成功」
		turn, err := s.opts.Service.GetTurn(r.Context(), turnID)
		if err != nil {
			writeSessionErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"turn_id": turnID,
			"stopped": false,
			"status":  string(turn.Status),
			"reason":  "轮次已结束",
		})
		return
	}

	s.opts.Service.CancelTurn(turnID)
	// 同时拒绝这轮挂起的审批：Request 虽然也会因 ctx 取消而返回，
	// 但那要等 ctx 传播；直接拒绝能让 agent 立刻拿到决定，也让
	// 前端弹窗立即消失而不是挂到超时。
	if s.opts.Approvals != nil {
		s.opts.Approvals.CancelTurn(turnID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"turn_id": turnID, "stopped": true})
}

// handleResumeTurn 续跑中断的轮次。
func (s *Server) handleResumeTurn(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")

	turn, err := s.opts.Service.GetTurn(r.Context(), turnID)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	log := s.opts.Streams.Get(turnID)
	if log == nil || log.Closed() {
		log = s.opts.Streams.Ensure(turnID)
	}

	go func() {
		res, err := s.opts.Service.ResumeTurnWith(s.opts.BaseContext, turn.SessionID, turnID,
			s.streamCallback(turnID, log), s.askOptions())
		log.Close(buildFinal(turnID, res, err))
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"turn_id": turnID})
}
