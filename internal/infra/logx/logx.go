// Package logx 提供结构化日志：控制台彩色可读，可选 JSON 文件落盘并轮转。
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Options 描述日志初始化参数。
type Options struct {
	Level      string // debug|info|warn|error
	File       string // 为空则不写文件
	MaxSizeMB  int    // 单文件超过该大小触发轮转
	MaxBackups int    // 保留的历史文件数
}

// New 返回系统日志器。控制台输出人类可读格式；File 非空时同时写 JSON 文件。
//
// 两级职责要分清：
//   - 系统日志（本函数返回的 logger）：装配、模型调用、工具执行、错误与降级。
//     必须可事后排查，所以落盘。
//   - 界面输出（渠道自己写 stdout）：用户对话、提示符、进度。
//     属于交互内容，不进日志文件，否则会被聊天内容淹没。
//
// 所有输出都经过脱敏 handler，密钥与令牌不会落盘。
func New(opts Options) (*slog.Logger, io.Closer, error) {
	level := parseLevel(opts.Level)

	var sinks []slog.Handler
	// 控制台日志走 stderr：stdout 留给渠道的界面输出，
	// 这样 REPL 的对话内容不会被日志打断，也能被管道正常接收。
	sinks = append(sinks, newConsoleHandler(os.Stderr, level))

	var closer io.Closer = nopCloser{}
	if opts.File != "" {
		if err := os.MkdirAll(filepath.Dir(opts.File), 0o755); err != nil {
			return nil, nil, fmt.Errorf("create log dir: %w", err)
		}
		w := newRotatingWriter(opts.File, opts.MaxSizeMB, opts.MaxBackups)
		sinks = append(sinks, slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
		closer = w
	}

	return slog.New(newRedactHandler(&multiHandler{handlers: sinks})), closer, nil
}

// Discard 返回一个不输出任何内容的 logger，测试用。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type multiHandler struct{ handlers []slog.Handler }

func (m *multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: next}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: next}
}

// consoleHandler 输出人类可读的一行式日志。
type consoleHandler struct {
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
}

func newConsoleHandler(w io.Writer, level slog.Level) *consoleHandler {
	return &consoleHandler{w: w, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteString(" ")
	b.WriteString(levelTag(r.Level))
	b.WriteString(" ")
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		writeAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteString("\n")
	_, err := io.WriteString(h.w, b.String())
	return err
}

func levelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERR "
	case l >= slog.LevelWarn:
		return "WARN"
	case l < slog.LevelInfo: // Debug(-4) 与任何自定义更低级别
		return "DBG "
	default:
		return "INFO"
	}
}

func writeAttr(b *strings.Builder, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	v := a.Value.Resolve()
	b.WriteString(" ")
	b.WriteString(a.Key)
	b.WriteString("=")
	b.WriteString(v.String())
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *consoleHandler) WithGroup(string) slog.Handler { return h }

// rotatingWriter 是带大小轮转的文件写入器。单用户场景不需要复杂实现。
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	f          *os.File
	size       int64
}

func newRotatingWriter(path string, maxSizeMB, maxBackups int) *rotatingWriter {
	if maxSizeMB <= 0 {
		maxSizeMB = 20
	}
	if maxBackups <= 0 {
		maxBackups = 5
	}
	w := &rotatingWriter{path: path, maxSize: int64(maxSizeMB) << 20, maxBackups: maxBackups}
	w.open()
	return w
}

func (w *rotatingWriter) open() {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
	}
	w.f = f
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		w.open()
		if w.f == nil {
			return len(p), nil // 日志失败不能影响主流程
		}
	}
	if w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	if w.f != nil {
		w.f.Close()
	}
	for i := w.maxBackups - 1; i >= 1; i-- {
		_ = os.Rename(backupName(w.path, i), backupName(w.path, i+1))
	}
	_ = os.Rename(w.path, backupName(w.path, 1))
	w.size = 0
	w.open()
}

func backupName(path string, i int) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s.%s%s", base, time.Now().Format("20060102")+"-"+itoa(i), ext)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
