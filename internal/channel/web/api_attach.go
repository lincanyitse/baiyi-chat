package web

import (
	"net/http"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/rag"
)

// maxAttachmentsTurn 是单轮附件数量上限。单文件大小上限由 readUpload
// 统一控制。
const maxAttachmentsTurn = 4

// 允许的附件类型。图片走视觉，文档进 RAG，其余作为文件块透传。
var (
	imageTypes = map[string]bool{
		"image/png":  true,
		"image/jpeg": true,
		"image/gif":  true,
		"image/webp": true,
	}
	docTypes = map[string]bool{
		"text/markdown":   true,
		"text/plain":      true,
		"text/html":       true,
		"application/pdf": true,
	}
)

// AttachmentDTO 是附件元信息的对外表示。
type AttachmentDTO struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Name      string `json:"name"`
	// Kind 是 image / document / file，前端据此选图标与提示。
	Kind string `json:"kind"`
}

// handleUploadAttachment 接收附件并存盘。
//
// 类型校验用 http.DetectContentType 嗅探真实内容，不信任扩展名与
// 客户端声明的 Content-Type——伪装成 .png 的脚本不该被当图片收下。
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	if s.opts.Attachments == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "附件功能未启用")
		return
	}

	data, name, ok := readUpload(w, r)
	if !ok {
		return
	}

	mediaType := sniffMediaType(data, name)
	kind := classify(mediaType, name)
	if kind == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
			"不支持的文件类型："+mediaType+"（支持 png/jpeg/gif/webp 与 md/txt/html/pdf）")
		return
	}

	id, err := s.opts.Attachments.Save(data, mediaType, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, AttachmentDTO{
		ID: id, MediaType: mediaType, Size: int64(len(data)),
		Name: name, Kind: kind,
	})
}

// handleGetAttachment 返回附件内容，供预览。
func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	if s.opts.Attachments == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "附件功能未启用")
		return
	}

	data, info, err := s.opts.Attachments.Load(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", info.MediaType)
	// 附件是用户自己的内容，但仍按不可信处理：禁止嗅探、禁止执行
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}

// sniffMediaType 判定真实类型，扩展名只作兜底。
//
// http.DetectContentType 只嗅探前 512 字节，且对文本类统一返回
// text/plain——markdown 与 html 需要靠扩展名补判。
func sniffMediaType(data []byte, name string) string {
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") || detected == "application/pdf" {
		return detected
	}

	// 文本类按扩展名细分。复用 rag 的映射表，避免两处维护同一份
	// 扩展名清单。
	if strings.HasPrefix(detected, "text/plain") || detected == "application/octet-stream" {
		if mt := rag.MIMEByExt(name); mt != "" && isTextLike(mt) {
			return mt
		}
		return "text/plain"
	}
	return detected
}

// isTextLike 判断扩展名推断出的类型是否真的是文本。
// MIMEByExt 也认 pdf/docx/json，那些不该被 text/plain 兜底覆盖。
func isTextLike(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json"
}

// classify 把 MIME 归到 image / document / file，不支持时返回空。
func classify(mediaType, name string) string {
	if imageTypes[mediaType] {
		return "image"
	}
	if docTypes[mediaType] {
		return "document"
	}
	return ""
}
