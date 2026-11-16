package logx

import (
	"context"
	"log/slog"
	"strings"
)

// redacted 是脱敏后的占位值。
const redacted = "***"

// sensitiveKeys 是属性名中包含即需要脱敏的关键词（小写匹配）。
//
// 覆盖：鉴权头、API Key、各类 token、密码、会话口令。
// 只要属性名命中，值一律替换为 ***，不依赖调用方自觉。
var sensitiveKeys = []string{
	"api_key", "apikey", "api-key",
	"authorization", "auth",
	"token", "secret", "password", "passwd",
	"credential", "cookie", "session_key",
	"x-api-key",
}

// redactHandler 包装下层 handler，在写出前把敏感属性替换为 ***。
//
// 这是 FR-OPS-006 / NFR-SEC-001 的落地点：所有日志都必须经过它，
// 这样后续新增的调试日志即使误把请求头整个打出来也不会泄露密钥。
type redactHandler struct {
	next slog.Handler
}

func newRedactHandler(next slog.Handler) slog.Handler {
	return &redactHandler{next: next}
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, redactText(r.Message), r.PC)

	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		safe[i] = redactAttr(a)
	}
	return &redactHandler{next: h.next.WithAttrs(safe)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if a.Equal(slog.Attr{}) {
		return a
	}
	v := a.Value.Resolve()

	if v.Kind() == slog.KindGroup {
		group := v.Group()
		safe := make([]slog.Attr, len(group))
		for i, ga := range group {
			safe[i] = redactAttr(ga)
		}
		a.Value = slog.GroupValue(safe...)
		return a
	}

	if isSensitive(a.Key) {
		return slog.String(a.Key, redacted)
	}

	// 值本身可能是 "Bearer sk-xxx" 或 URL 里的 key=...，做一次文本兜底。
	if v.Kind() == slog.KindString {
		a.Value = slog.StringValue(redactText(v.String()))
	}
	return a
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// redactText 处理值里内嵌的凭据：Bearer 令牌与 URL 查询参数中的 key。
func redactText(s string) string {
	if s == "" {
		return s
	}
	out := s

	// Bearer <token>
	for _, prefix := range []string{"Bearer ", "bearer "} {
		if i := strings.Index(out, prefix); i >= 0 {
			out = out[:i+len(prefix)] + redacted + redactTailAfterToken(out[i+len(prefix):])
		}
	}

	// 常见查询参数：?api_key=xxx / &key=xxx / &token=xxx
	for _, param := range []string{"api_key=", "apikey=", "key=", "token=", "access_token="} {
		out = redactQueryParam(out, param)
	}
	return out
}

// redactTailAfterToken 保留令牌后的空白或分隔符，避免破坏日志可读性。
func redactTailAfterToken(rest string) string {
	for i, r := range rest {
		if r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' || r == ',' {
			return rest[i:]
		}
	}
	return ""
}

func redactQueryParam(s, param string) string {
	start := 0
	for {
		i := strings.Index(s[start:], param)
		if i < 0 {
			return s
		}
		i += start + len(param)
		end := i
		for end < len(s) {
			c := s[end]
			if c == '&' || c == ' ' || c == '"' || c == '\'' || c == '#' || c == '\n' || c == '\t' {
				break
			}
			end++
		}
		s = s[:i] + redacted + s[end:]
		start = i + len(redacted)
	}
}
