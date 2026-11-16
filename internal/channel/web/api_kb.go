package web

import (
	"net/http"

	"github.com/lincanyitse/baiyi-chat/internal/rag"
)

// DocumentDTO 是知识库文档的对外表示。
type DocumentDTO struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Title      string `json:"title"`
	MIME       string `json:"mime"`
	SizeBytes  int64  `json:"size_bytes"`
	CharCount  int    `json:"char_count"`
	ChunkCount int    `json:"chunk_count"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	EmbedModel string `json:"embed_model,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

func toDocumentDTO(d rag.Document) DocumentDTO {
	return DocumentDTO{
		ID: d.ID, Source: d.Source, Title: d.Title, MIME: d.MIME,
		SizeBytes: d.SizeBytes, CharCount: d.CharCount, ChunkCount: d.ChunkCount,
		Status: string(d.Status), Error: d.Error, EmbedModel: d.EmbedModel,
		CreatedAt: d.CreatedAt.UnixMilli(), UpdatedAt: d.UpdatedAt.UnixMilli(),
	}
}

// ragAvailable 报告知识库是否装配。
func (s *Server) ragAvailable() bool {
	return s.opts.RAG != nil && s.opts.RAG.Repo != nil
}

// handleListDocuments 列出知识库文档。
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.ragAvailable() {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "知识库未启用")
		return
	}

	docs, total, err := s.opts.RAG.Repo.ListDocumentsFiltered(
		r.Context(), r.URL.Query().Get("status"), queryInt(r, "offset", 0), queryInt(r, "limit", 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	out := make([]DocumentDTO, 0, len(docs))
	for _, d := range docs {
		out = append(out, toDocumentDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": out, "total": total})
}

// handleDeleteDocument 删除文档及其切分与向量。
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if !s.ragAvailable() {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "知识库未启用")
		return
	}

	if err := s.opts.RAG.Repo.DeleteDocument(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUploadDocument 上传文本文件并入库。
//
// 解析与切分是同步的：上传返回时文档已经可检索，只有向量化在后台跑。
// 表单字段 file，限制 8 MiB。
func (s *Server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	if !s.ragAvailable() {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "知识库未启用")
		return
	}
	if s.opts.RAG.Ingester == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeUnavailable, "入库服务不可用")
		return
	}

	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}

	doc, err := s.opts.RAG.Ingester.IngestBytes(r.Context(), name, "", data)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	// Ingester 返回的 Document 不含片段数；重查一次让响应与列表一致
	if fresh, err := s.opts.RAG.Repo.GetDocument(r.Context(), doc.ID); err == nil {
		doc = fresh
	}
	if n, err := s.opts.RAG.Repo.ChunkCount(r.Context(), doc.ID); err == nil {
		doc.ChunkCount = n
	}
	writeJSON(w, http.StatusCreated, toDocumentDTO(doc))
}

// handleDocumentChunks 返回文档的切分片段。
func (s *Server) handleDocumentChunks(w http.ResponseWriter, r *http.Request) {
	if !s.ragAvailable() {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "知识库未启用")
		return
	}

	chunks, err := s.opts.RAG.Repo.ChunksByDocument(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	}

	type chunkDTO struct {
		ID    string `json:"id"`
		Ord   int    `json:"ord"`
		Text  string `json:"text"`
		Chars int    `json:"chars"`
	}
	out := make([]chunkDTO, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, chunkDTO{ID: c.ID, Ord: c.Ordinal, Text: c.Text, Chars: len([]rune(c.Text))})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleKBSearch 检索测试：直接跑一次检索，看命中什么。
func (s *Server) handleKBSearch(w http.ResponseWriter, r *http.Request) {
	if !s.ragAvailable() || s.opts.RAG.Retriever == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "知识库未启用")
		return
	}

	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Query == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "query 不能为空")
		return
	}

	res, err := s.opts.RAG.Retriever.Search(r.Context(), in.Query, in.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	type hitDTO struct {
		ChunkID     string  `json:"chunk_id"`
		DocID       string  `json:"document_id"`
		Title       string  `json:"title"`
		HeadingPath string  `json:"heading_path,omitempty"`
		Text        string  `json:"text"`
		Score       float64 `json:"score"`
	}
	out := make([]hitDTO, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		out = append(out, hitDTO{
			ChunkID: c.ChunkID, DocID: c.DocumentID, Title: c.Title,
			HeadingPath: c.HeadingPath, Text: c.Snippet, Score: float64(c.Score),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hits":     out,
		"degraded": res.Degraded,
		"reason":   res.Reason,
	})
}

// handleListJobs 返回后台任务，供上传后查看向量化进度。
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if s.opts.RAG == nil || s.opts.RAG.JobStore == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	kind := r.URL.Query().Get("kind")
	list, err := s.opts.RAG.JobStore.ListRecent(r.Context(), kind, queryInt(r, "limit", 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	type jobDTO struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Error     string `json:"error,omitempty"`
		CreatedAt int64  `json:"created_at"`
		UpdatedAt int64  `json:"updated_at"`
	}
	out := make([]jobDTO, 0, len(list))
	for _, j := range list {
		out = append(out, jobDTO{
			ID: j.ID, Kind: j.Kind, Status: string(j.Status), Error: j.LastError,
			CreatedAt: j.CreatedAt.UnixMilli(), UpdatedAt: j.UpdatedAt.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
