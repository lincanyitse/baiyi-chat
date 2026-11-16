package web

import (
	"net/http"
)

// handleResetSecrets 清空全部加密密钥并重新生成数据密钥。
//
// 用在「数据密钥解不开」之后（换了系统用户、换了机器、密钥文件被替换）。
// 这是破坏性操作，前端必须二次确认。
func (s *Server) handleResetSecrets(w http.ResponseWriter, r *http.Request) {
	if s.opts.Secrets == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "加密存储不可用")
		return
	}
	if err := s.opts.Secrets.Reset(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "已清空全部密钥，请重新填写各模型服务的 API Key",
	})
}

// handleSecretsStatus 报告加密存储是否可用、用了哪种保护方式。
//
// 界面用它决定是否显示「重置密钥」入口，以及是否给出「保护强度有限」
// 的提示——非 Windows 平台不是系统级保护，不能给出超出实际的承诺。
func (s *Server) handleSecretsStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Secrets == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "加密存储未初始化；只能通过环境变量提供 API Key",
		})
		return
	}
	protection := string(s.opts.Secrets.Protection())
	writeJSON(w, http.StatusOK, map[string]any{
		"available":  true,
		"protection": protection,
		"note":       protectionNote(protection),
	})
}

// protectionNote 说明当前保护方式的真实强度。
//
// 非 Windows 平台只做了文件绑定，不说清楚等于给了虚假的安全感。
func protectionNote(protection string) string {
	switch protection {
	case "dpapi":
		return "密钥由 Windows DPAPI 保护，绑定当前用户。换用户或换机器后需要重新填写。"
	case "file-binding":
		return "当前平台无法使用系统级密钥保护，密钥仅与数据目录中的密钥文件绑定。" +
			"文件被复制到别处会失效；能读取该文件的程序仍可解密。需要更强保护请使用 Windows 或改用环境变量。"
	default:
		return ""
	}
}
