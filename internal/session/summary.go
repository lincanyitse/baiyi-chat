package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
)

// llmSummarizer 用模型生成结构化摘要。
//
// 走 route.summarize 路由：摘要属于内部任务，应使用便宜模型，
// 缺省时回落到 chat 候选链。
type llmSummarizer struct {
	reg *registry.Registry
}

var _ contextx.Summarizer = (*llmSummarizer)(nil)

// Summarize 调用模型把历史压缩成六段式摘要。
func (s *llmSummarizer) Summarize(ctx context.Context, msgs []llm.Message) (string, error) {
	provider, ref, err := s.reg.Resolve("summarize")
	if err != nil {
		return "", err
	}

	// 把历史渲染成纯文本喂给摘要模型
	var b strings.Builder
	for _, m := range msgs {
		text := llm.TextOf(m)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n\n", m.Role, text)
	}

	req := llm.Request{
		Model:  ref.Model,
		System: contextx.SummaryPrompt(),
		Messages: []llm.Message{
			llm.NewText(llm.RoleUser, "请压缩以下对话历史：\n\n"+b.String()),
		},
		Stream: true,
	}
	if caps := provider.Capabilities(ref.Model); caps.MaxOutput > 0 {
		req.MaxTokens = caps.MaxOutput
	}

	events, err := provider.Stream(ctx, req)
	if err != nil {
		return "", llm.AsError(err)
	}

	var out strings.Builder
	for ev := range events {
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			out.WriteString(e.Text)
		case llm.ErrorEvent:
			return "", e.Err
		}
	}

	if err := contextx.ValidateSummary(out.String()); err != nil {
		return "", err
	}
	return out.String(), nil
}

// 让 Service 直接满足 contextx.SummaryRepo。
var _ contextx.SummaryRepo = (*Service)(nil)

// LoadSummary 读取会话摘要状态。
func (s *Service) LoadSummary(ctx context.Context, sessionID string) (contextx.SummaryState, bool, error) {
	return s.repo.LoadSummary(ctx, sessionID)
}

// SaveSummary 写入会话摘要状态。
func (s *Service) SaveSummary(ctx context.Context, st contextx.SummaryState) error {
	return s.repo.SaveSummary(ctx, st)
}
