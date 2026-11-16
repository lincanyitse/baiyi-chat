package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/export"
)

// BackupFile 是备份文件的信息，由 bootstrap 填充。
//
// 在 web 包内定义而不是复用 bootstrap.BackupInfo：channel 层不该
// 依赖 bootstrap，否则分层方向就反了。
type BackupFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// BackupInfoDTO 是备份文件的对外描述。
type BackupInfoDTO struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// handleExportSession 导出单个会话。
//
// format 决定产物：md（默认，给人看）或 jsonl（给程序读）。
func (s *Server) handleExportSession(w http.ResponseWriter, r *http.Request) {
	if s.opts.ExportSession == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "导出未启用")
		return
	}

	id := r.PathValue("id")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "md"
	}

	doc, filename, err := s.opts.ExportSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	}

	var (
		body        []byte
		contentType string
		ext         string
	)
	switch format {
	case "md", "markdown":
		body = []byte(export.Markdown(doc))
		contentType = "text/markdown; charset=utf-8"
		ext = "md"
	case "jsonl":
		body, err = export.JSONL(doc)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
			return
		}
		contentType = "application/x-ndjson; charset=utf-8"
		ext = "jsonl"
	default:
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "未知格式: "+format)
		return
	}

	writeDownload(w, contentType, safeFilename(filename, ext), body)
}

// handleExportAll 导出全部会话为 JSONL。
//
// 只做 JSONL：全部会话的 Markdown 拼在一起没有阅读价值，
// 而这个接口的用途是「把数据带走」。
func (s *Server) handleExportAll(w http.ResponseWriter, r *http.Request) {
	if s.opts.ExportSession == nil || s.opts.ListSessionsForExport == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "导出未启用")
		return
	}

	ids, err := s.opts.ListSessionsForExport(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	docs := make([]export.Doc, 0, len(ids))
	for _, id := range ids {
		doc, _, err := s.opts.ExportSession(r.Context(), id)
		if err != nil {
			// 单个会话读不出来不该让整次导出失败，跳过它
			s.opts.Log.Warn("导出时跳过一个会话", "id", id, "err", err)
			continue
		}
		docs = append(docs, doc)
	}

	body, err := export.JSONLAll(docs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	writeDownload(w, "application/x-ndjson; charset=utf-8",
		safeFilename("全部会话", "jsonl"), body)
}

// handleCreateBackup 立即生成一份备份。
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Backup == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "备份未启用")
		return
	}
	info, err := s.opts.Backup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BackupInfoDTO{
		Path: info.Path, Size: info.Size, ModTime: info.ModTime.Format("2006-01-02 15:04:05"),
	})
}

// handleListBackups 列出已有备份。
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if s.opts.ListBackups == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "备份未启用")
		return
	}
	all, err := s.opts.ListBackups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	out := make([]BackupInfoDTO, 0, len(all))
	for _, b := range all {
		out = append(out, BackupInfoDTO{
			Path: b.Path, Size: b.Size, ModTime: b.ModTime.Format("2006-01-02 15:04:05"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": out})
}

// writeDownload 写出一个附件下载响应。
func writeDownload(w http.ResponseWriter, contentType, filename string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// safeFilename 由会话标题生成下载文件名：清掉路径分隔符与控制字符，
// 追加扩展名。文件名只用于 Content-Disposition，不参与任何路径拼接。
func safeFilename(title, ext string) string {
	const maxLen = 60
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r < 0x20:
			// 控制字符直接丢弃
		default:
			b.WriteRune(r)
		}
		if b.Len() >= maxLen {
			break
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = "会话"
	}
	return name + "." + ext
}
