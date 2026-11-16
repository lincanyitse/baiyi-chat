package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// handleListSessions 返回会话列表，支持搜索与分页。
//
// 参数：
//   - q：标题关键词（子串、不区分大小写）
//   - include_archived=true：连已归档的一起返回
//   - limit / offset：分页
//
// 带 q 或 offset 时返回 {items, total}；不带时保持旧的数组形状，
// 避免老前端与 curl 脚本被改动波及。
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	query := q.Get("q")
	includeArchived := q.Get("include_archived") == "true"

	page, err := s.opts.Service.SearchSessions(r.Context(), session.SessionQuery{
		Query:           query,
		IncludeArchived: includeArchived,
		Limit:           limit,
		Offset:          offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	if query == "" && offset == 0 {
		// 兼容旧形状：只返回数组
		writeJSON(w, http.StatusOK, toSessionDTOs(page.Items))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": toSessionDTOs(page.Items),
		"total": page.Total,
	})
}

// handleCreateSession 新建会话。
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	// 允许空 body
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}

	sess, err := s.opts.Service.CreateSession(r.Context(), in.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toSessionDTO(sess))
}

// handleUpdateSession 重命名或归档。
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var in struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
		// Model 是会话级模型覆盖，"provider/model"；空串表示跟随路由。
		Model *string `json:"model"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	ctx := r.Context()
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "标题不能为空")
			return
		}
		if err := s.opts.Service.RenameSession(ctx, id, title); err != nil {
			writeSessionErr(w, err)
			return
		}
	}
	if in.Archived != nil {
		if err := s.opts.Service.ArchiveSession(ctx, id, *in.Archived); err != nil {
			writeSessionErr(w, err)
			return
		}
	}
	if in.Model != nil {
		if err := s.opts.Service.SetSessionModel(ctx, id, strings.TrimSpace(*in.Model)); err != nil {
			writeSessionErr(w, err)
			return
		}
	}

	sess, err := s.opts.Service.GetSession(ctx, id)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDTO(sess))
}

// handleDeleteSession 软删除会话。
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Service.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		writeSessionErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMessages 返回会话的消息历史。
//
// 默认返回当前分支（最新叶子所在的那条链）；带 branch={leafID}
// 时返回指定分支。前端画分支切换器需要完整树，走 /tree 接口。
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 先确认会话存在：History 对不存在的会话返回空列表，
	// 前端无法区分「会话不存在」与「会话还没有消息」。
	if _, err := s.opts.Service.GetSession(r.Context(), id); err != nil {
		writeSessionErr(w, err)
		return
	}

	leaf := r.URL.Query().Get("branch")
	msgs, err := s.opts.Service.HistoryAt(r.Context(), id, leaf)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	out := make([]MessageDTO, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toMessageDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSearchMessages 在单个会话里搜消息正文。
//
// 返回命中的消息及其 seq，前端据此跳到对应位置。只返回有命中的消息，
// 不返回上下文——会话里本来就有全部消息，前端自己取相邻的。
func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "缺少搜索词 q")
		return
	}

	limit := queryInt(r, "limit", 50)
	msgs, err := s.opts.Service.SearchMessages(r.Context(), id, q, limit)
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	out := make([]MessageDTO, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toMessageDTO(llm.Message{
			ID: m.ID, Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt,
			Meta: llm.MessageMeta{ParentID: m.ParentID, Trust: m.Trust},
		}))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":    q,
		"messages": out,
	})
}

// handleMessageTree 返回完整消息树，含每条消息的兄弟位置。
func (s *Server) handleMessageTree(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.opts.Service.GetSession(r.Context(), id); err != nil {
		writeSessionErr(w, err)
		return
	}

	tree, err := s.opts.Service.MessageTreeOf(r.Context(), id, r.URL.Query().Get("branch"))
	if err != nil {
		writeSessionErr(w, err)
		return
	}

	nodes := make([]MessageNodeDTO, 0, len(tree.Chain))
	for _, m := range tree.Chain {
		node := tree.Nodes[m.ID]
		nodes = append(nodes, MessageNodeDTO{
			MessageDTO: toMessageDTO(m),
			Siblings:   node.Branch.Siblings,
			Index:      node.Branch.Index,
			Children:   node.Children,
		})
	}

	writeJSON(w, http.StatusOK, MessageTreeDTO{Leaf: tree.Leaf, Messages: nodes})
}

// writeSessionErr 把 session 层错误映射成 HTTP 状态码。
//
// 判据是哨兵错误（errors.Is）而不是错误文本：文案随时可能调整，
// 状态码是协议的一部分，不该跟着文案一起变。
func writeSessionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
	case errors.Is(err, session.ErrInvalid):
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
}

// queryInt 读整型查询参数，缺失或非法时返回默认值。
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
