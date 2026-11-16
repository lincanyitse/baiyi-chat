package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxErrorBodyBytes 是读取错误响应体的上限，防止异常大响应耗尽内存。
const maxErrorBodyBytes = 64 * 1024

// ClassifyStatus 把 HTTP 状态码映射为统一的错误分类。
//
// 这是所有适配器共用的唯一状态码表：任何新增或调整都只改这里，
// 避免各适配器各自维护一份而悄悄分叉。
func ClassifyStatus(status int) ErrorKind {
	switch {
	case status == 401 || status == 403:
		return ErrAuth
	case status == 402:
		return ErrQuota
	case status == 429:
		return ErrRateLimited
	case status == 400 || status == 404 || status == 413 || status == 422:
		return ErrInvalidRequest
	case status >= 500:
		return ErrTransient
	default:
		return ErrUnknown
	}
}

// ParseHTTPError 把非 2xx 响应归一化为 *Error。
//
// 它会读取并保留原始报文（上限 64KB），解析 Retry-After，
// 并尝试从常见的错误包装格式中提取可读消息。
// 厂商特有的嵌套格式由调用方在返回后进一步处理。
func ParseHTTPError(resp *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	e := &Error{
		Kind:       ClassifyStatus(resp.StatusCode),
		HTTPStatus: resp.StatusCode,
		Message:    Truncate(string(body), 800),
		Raw:        body,
	}
	e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))

	// 标准 OpenAI 风格：{"error":{"message":"...","type":"...","code":...}}
	var wrapper struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wrapper) == nil && wrapper.Error.Message != "" {
		e.Message = Truncate(wrapper.Error.Message, 800)
		if wrapper.Error.Type != "" {
			e.VendorCode = wrapper.Error.Type
		} else if wrapper.Error.Code != nil {
			e.VendorCode = fmt.Sprint(wrapper.Error.Code)
		}
	}
	return e
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v + "s"); err == nil {
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Truncate 按字节截断字符串并追加省略号。用于日志与错误消息。
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
