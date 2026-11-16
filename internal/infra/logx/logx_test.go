package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureHandler 把记录转成可断言的文本。
type captureHandler struct {
	buf *bytes.Buffer
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.buf.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		h.buf.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	h.buf.WriteString("\n")
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func newCapture() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(newRedactHandler(&captureHandler{buf: buf})), buf
}

func TestRedactsSensitiveKeys(t *testing.T) {
	logger, buf := newCapture()
	logger.Info("请求",
		"api_key", "sk-real-secret",
		"authorization", "Bearer abc123",
		"token", "tok-123",
		"password", "hunter2",
		"model", "glm-5.3-flash",
	)

	out := buf.String()
	for _, secret := range []string{"sk-real-secret", "abc123", "tok-123", "hunter2"} {
		if strings.Contains(out, secret) {
			t.Errorf("日志泄露了 %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "glm-5.3-flash") {
		t.Errorf("非敏感字段应保留原文:\n%s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Errorf("应出现脱敏占位符:\n%s", out)
	}
}

func TestRedactsBearerInValue(t *testing.T) {
	logger, buf := newCapture()
	logger.Info("上游返回", "detail", "Authorization: Bearer sk-abcdef123456 more")

	out := buf.String()
	if strings.Contains(out, "sk-abcdef123456") {
		t.Errorf("值内嵌的 Bearer 令牌应被脱敏:\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("令牌后的内容应保留:\n%s", out)
	}
}

func TestRedactsQueryParams(t *testing.T) {
	logger, buf := newCapture()
	logger.Info("请求", "url", "https://api.example.com/v1?api_key=secret123&model=x")

	out := buf.String()
	if strings.Contains(out, "secret123") {
		t.Errorf("URL 查询参数中的密钥应被脱敏:\n%s", out)
	}
	if !strings.Contains(out, "model=x") {
		t.Errorf("非敏感查询参数应保留:\n%s", out)
	}
}

func TestRedactsNestedGroup(t *testing.T) {
	logger, buf := newCapture()
	logger.With("auth", slog.Group("headers",
		slog.String("authorization", "Bearer leak-me"),
		slog.String("content-type", "application/json"),
	)).Info("ok")

	out := buf.String()
	if strings.Contains(out, "leak-me") {
		t.Errorf("嵌套分组中的密钥应被脱敏:\n%s", out)
	}
}

func TestRedactsWithAttrs(t *testing.T) {
	logger, buf := newCapture()
	child := logger.With("api_key", "sk-from-with")
	child.Info("子日志")

	out := buf.String()
	if strings.Contains(out, "sk-from-with") {
		t.Errorf("With 绑定的密钥应被脱敏:\n%s", out)
	}
}

func TestConsoleOutputReadable(t *testing.T) {
	var buf bytes.Buffer
	h := newConsoleHandler(&buf, slog.LevelInfo)
	logger := slog.New(newRedactHandler(h))
	logger.Info("测试消息", "key", "value")

	out := buf.String()
	if !strings.Contains(out, "INFO") || !strings.Contains(out, "测试消息") || !strings.Contains(out, "key=value") {
		t.Errorf("控制台输出格式不对:\n%s", out)
	}
}

func TestFileOutputIsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baiyi.log")

	logger, closer, err := New(Options{Level: "info", File: path})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("结构化", "count", 3, "api_key", "sk-should-not-appear")
	closer.Close()

	data, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) != 1 {
		t.Fatalf("期望 1 行日志，实际 %d", len(lines))
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("日志文件应为 JSON 行: %v\n%s", err, lines[0])
	}
	if entry["msg"] != "结构化" {
		t.Errorf("msg = %v", entry["msg"])
	}
	if strings.Contains(data, "sk-should-not-appear") {
		t.Errorf("写入文件的日志也应脱敏:\n%s", data)
	}
}

func TestRotationKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baiyi.log")

	w := newRotatingWriter(path, 1, 3)
	defer w.Close()

	// 每次写入 ~200KB，写到 2MB 会触发轮转。
	chunk := bytes.Repeat([]byte("x"), 200*1024)
	for i := 0; i < 12; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.log*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Errorf("应发生轮转，实际只有 %d 个文件", len(files))
	}
	if len(files) > 4 {
		t.Errorf("轮转文件数超出上限: %d", len(files))
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	h := newConsoleHandler(&buf, slog.LevelWarn)
	logger := slog.New(newRedactHandler(h))

	logger.Debug("不该出现")
	logger.Info("不该出现")
	logger.Warn("应该出现")

	out := buf.String()
	if strings.Contains(out, "不该出现") {
		t.Errorf("低于阈值的日志不应输出:\n%s", out)
	}
	if !strings.Contains(out, "应该出现") {
		t.Errorf("达标日志应输出:\n%s", out)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
