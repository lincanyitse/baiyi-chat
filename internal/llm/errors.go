package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorKind 是归一化后的错误分类，驱动重试与降级策略。
type ErrorKind string

const (
	ErrTransient       ErrorKind = "transient"        // 网络重置、5xx、连接超时
	ErrRateLimited     ErrorKind = "rate_limited"     // 429
	ErrAuth            ErrorKind = "auth"             // 401/403、密钥缺失
	ErrQuota           ErrorKind = "quota"            // 余额不足/额度耗尽
	ErrInvalidRequest  ErrorKind = "invalid_request"  // 400、schema 不符
	ErrContextOverflow ErrorKind = "context_overflow" // 超长
	ErrPolicy          ErrorKind = "policy"           // 内容审核拒绝
	ErrNotSupported    ErrorKind = "not_supported"    // 能力缺失
	ErrCanceled        ErrorKind = "canceled"         // 用户取消
	ErrUnknown         ErrorKind = "unknown"
)

// Error 是跨 Provider 的统一错误。
type Error struct {
	Kind       ErrorKind     `json:"kind"`
	HTTPStatus int           `json:"http_status,omitempty"`
	VendorCode string        `json:"vendor_code,omitempty"`
	Message    string        `json:"message"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Raw        []byte        `json:"-"`
}

func (e *Error) Error() string {
	if e.VendorCode != "" {
		return fmt.Sprintf("llm %s (http %d, vendor %s): %s", e.Kind, e.HTTPStatus, e.VendorCode, e.Message)
	}
	return fmt.Sprintf("llm %s (http %d): %s", e.Kind, e.HTTPStatus, e.Message)
}

// Retryable 表示该错误是否值得重试。
func (e *Error) Retryable() bool {
	switch e.Kind {
	case ErrTransient, ErrRateLimited:
		return true
	default:
		return false
	}
}

// Fallbackable 表示该错误是否应触发路由降级到下一候选模型。
func (e *Error) Fallbackable() bool {
	switch e.Kind {
	case ErrTransient, ErrRateLimited, ErrQuota, ErrAuth, ErrNotSupported:
		return true
	default:
		return false
	}
}

// AsError 尝试把任意 error 归一化为 *Error。
//
// 非 *Error 的错误按成因分类：网络层失败通常是瞬时的，
// 归为 unknown 会让降级与重试双双失效。
func AsError(err error) *Error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}

	msg := err.Error()
	kind := classifyPlainError(msg)
	return &Error{Kind: kind, Message: msg}
}

// classifyPlainError 依据错误文本推断分类。
//
// 这里必须宽松：不同平台的网络错误文本差异极大
// （Linux 用 "connection refused"，Windows 用 "actively refused it"），
// 漏判会让降级与重试双双失效。
func classifyPlainError(msg string) ErrorKind {
	lower := strings.ToLower(msg)

	// 用户主动取消优先判断——它不是故障
	if strings.Contains(lower, "context canceled") {
		return ErrCanceled
	}

	// 连接失败、DNS、超时、连接重置都属于瞬时故障
	for _, s := range []string{
		"connection refused", "actively refused", "connection reset",
		"no such host", "name resolution", "i/o timeout", "timeout",
		"temporary failure", "network is unreachable", "broken pipe",
		"eof", "dial tcp", "dial udp", "connection closed",
	} {
		if strings.Contains(lower, s) {
			return ErrTransient
		}
	}
	return ErrUnknown
}

// NewError 构造统一错误。
func NewError(kind ErrorKind, status int, msg string) *Error {
	return &Error{Kind: kind, HTTPStatus: status, Message: msg}
}
