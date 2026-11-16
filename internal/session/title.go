package session

// 用模型给会话起名。
//
// 与「截断首条消息」的区别：截断常常得到「帮我看看这个」这种
// 无法区分的标题，会话一多就分不清。生成式命名能给出
// 「修复 SSE 断线续传」这样的主题。
//
// 三条硬约束：
//   1. 必须有超时——命名是锦上添花，不能拖住主流程。
//   2. 必须有降级——模型不可用时退回截断，绝不因此报错。
//   3. 必须便宜——只发一次、限定 MaxOutput，不记入用户可见的成本。

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
)

const (
	// titleTimeout 限制命名调用的总耗时。
	titleTimeout = 15 * time.Second
	// titleMaxRunes 是标题长度上限（按字符而非字节，中文才不会被截半）。
	titleMaxRunes = 24
)

// titlePrompt 是命名用的系统提示。
//
// 要求「只输出标题」而不是让模型自由发挥：任何多余的解释都会被
// 塞进会话列表。
const titlePrompt = `你是一个会话命名器。根据用户的第一句话，给出一个 6-14 个字的主题标题。

规则：
- 只输出标题本身，不要引号、不要句号、不要解释
- 用名词短语概括主题，不要复述原句
- 如果是提问，概括问题主题而不是照抄问题`

// GenerateTitle 用模型给会话生成标题。
//
// 失败时返回空串与错误，调用方应退回 truncateTitle。
func (s *Service) GenerateTitle(ctx context.Context, firstUserText string) (string, error) {
	if s.reg == nil {
		return "", fmt.Errorf("模型注册表不可用")
	}

	provider, ref, err := s.reg.Resolve("chat")
	if err != nil {
		return "", err
	}

	// 命名是小任务：用单独的短超时，失败不影响主流程
	ctx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()

	req := llm.Request{
		Model:     ref.Model,
		System:    titlePrompt,
		MaxTokens: 64,
		Stream:    true,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{llm.NewTextBlock(truncateForTitle(firstUserText))},
		}},
	}

	ch, err := provider.Stream(ctx, req)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for ev := range ch {
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			b.WriteString(e.Text)
		case llm.ErrorEvent:
			if e.Err != nil {
				return "", fmt.Errorf("命名请求失败: %s", e.Err.Message)
			}
			return "", fmt.Errorf("命名请求失败")
		}
	}

	title := cleanTitle(b.String())
	if title == "" {
		return "", fmt.Errorf("模型没有给出可用标题")
	}
	return title, nil
}

// truncateForTitle 限制送进模型的原文长度。
//
// 命名只需要第一句的主题，把整段长文送进去既浪费 token，
// 也可能让模型被细节带偏。
func truncateForTitle(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= 500 {
		return s
	}
	return string(r[:500])
}

// cleanTitle 清洗模型输出：去掉引号、换行、Markdown 标记，限长。
func cleanTitle(raw string) string {
	s := strings.TrimSpace(raw)
	// 只取第一行：模型偶尔会加解释
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)

	// 去掉成对引号与常见的 Markdown 包裹
	s = strings.Trim(s, "\"'`「」『』*# ")
	s = strings.TrimSuffix(s, "。")
	s = strings.TrimSpace(s)

	// 去掉控制字符
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)

	r := []rune(s)
	if len(r) > titleMaxRunes {
		s = string(r[:titleMaxRunes])
	}
	return strings.TrimSpace(s)
}

// registryRef 只是为了让编译期确认 registry 包仍被使用。
var _ = registry.ModelRef{}
