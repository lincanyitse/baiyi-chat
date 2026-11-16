package web

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// handleDiagnostics 打包一份排障用的诊断包。
//
// 内容：系统信息、脱敏后的配置、最近的日志。不含数据库、
// 不含会话内容——诊断看的是「环境对不对」，不是「用户聊了什么」。
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	writeZipFile := func(name string, body []byte) {
		f, err := zw.Create(name)
		if err != nil {
			return
		}
		_, _ = f.Write(body)
	}

	// 1. 系统信息
	info := fmt.Sprintf(`白泽诊断包
生成时间: %s
版本: %s
Go: %s
平台: %s/%s
运行时长: %d 秒
数据目录: %s
配置路径: %s
日志文件: %s
`,
		time.Now().Format("2006-01-02 15:04:05"),
		s.opts.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH,
		int64(time.Since(s.startAt).Seconds()),
		s.opts.DataDir, s.opts.ConfigPath, s.opts.LogFile,
	)
	writeZipFile("system.txt", []byte(info))

	// 2. 脱敏后的配置
	if s.opts.Files != nil && s.opts.ConfigPath != "" {
		if raw, err := s.opts.Files.ReadFile(s.opts.ConfigPath); err == nil {
			writeZipFile("config.toml", redactConfig(raw))
		}
	}

	// 3. 最近的日志（当前文件 + 最多两个轮转文件）
	if s.opts.Files != nil && s.opts.LogFile != "" {
		for _, p := range recentLogs(s.opts.LogFile) {
			// 只取最后 2 MiB：日志可能已经很大，诊断不需要全量
			if raw, err := s.opts.Files.ReadFile(p); err == nil {
				writeZipFile(filepath.Base(p), tailBytes(raw, 2<<20))
			}
		}
	}

	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}

	name := "baiyi-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	writeDownload(w, "application/zip", name, buf.Bytes())
}

// redactConfig 把配置里的敏感值替换掉。
//
// 配置文件本身不含 API Key（密钥在数据库里），但可能有自定义 header、
// 内网地址。按行做保守替换：任何看起来像凭证的键一律抹掉值。
func redactConfig(raw []byte) []byte {
	sensitive := []string{
		"api_key", "apikey", "token", "secret", "password", "authorization",
		"apikey", "key =",
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "=") {
			continue
		}
		for _, key := range sensitive {
			if strings.Contains(trimmed, key) {
				if idx := strings.Index(line, "="); idx >= 0 {
					lines[i] = line[:idx+1] + ` "***"`
				}
				break
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// recentLogs 返回当前日志文件与最多两个轮转文件。
func recentLogs(path string) []string {
	out := []string{path}
	for i := 1; i <= 2; i++ {
		out = append(out, fmt.Sprintf("%s.%d", path, i))
	}
	return out
}

// tailBytes 取字节尾部，避免诊断包被历史日志撑大。
func tailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	cut := len(b) - n
	// 从行边界切起，避免第一行是半截
	if i := bytes.IndexByte(b[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	return b[cut:]
}
