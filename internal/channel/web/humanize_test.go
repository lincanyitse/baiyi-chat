package web

import (
	"errors"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// TestHumanizeErrorTranslatesKinds 确认每类错误都翻成可照做的中文，
// 而不是把 `llm transient (http 0): dial tcp...` 原样丢给用户。
func TestHumanizeErrorTranslatesKinds(t *testing.T) {
	cases := []struct {
		kind llm.ErrorKind
		want string
	}{
		{llm.ErrTransient, "连不上模型服务"},
		{llm.ErrAuth, "认证失败"},
		{llm.ErrRateLimited, "限流"},
		{llm.ErrQuota, "额度"},
		{llm.ErrContextOverflow, "上下文窗口"},
		{llm.ErrNotSupported, "不支持"},
	}

	for _, c := range cases {
		err := llm.NewError(c.kind, 0, "raw vendor message")
		got := humanizeError(err)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: 期望包含 %q，得到 %q", c.kind, c.want, got)
		}
		if strings.Contains(got, "llm ") {
			t.Errorf("%s: 不该把内部前缀暴露给用户，得到 %q", c.kind, got)
		}
	}
}

// TestHumanizeErrorKeepsUnknownMessage 确认未知分类仍保留原文，
// 让用户至少能在日志里搜到。
func TestHumanizeErrorKeepsUnknownMessage(t *testing.T) {
	got := humanizeError(errors.New("something weird happened"))
	if !strings.Contains(got, "something weird happened") {
		t.Fatalf("未知错误应保留原文，得到 %q", got)
	}
}

// TestHumanizeErrorClassifiesPlainNetworkError 确认非 *llm.Error 的
// 网络错误也能被识别——这是最常见的失败方式。
func TestHumanizeErrorClassifiesPlainNetworkError(t *testing.T) {
	got := humanizeError(errors.New(`Post "http://x": dial tcp 127.0.0.1:1: connect: connection refused`))
	if !strings.Contains(got, "连不上模型服务") {
		t.Fatalf("连接失败应翻成中文，得到 %q", got)
	}
}
