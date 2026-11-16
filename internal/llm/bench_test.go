package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// 基准：token 估算在 M1 的上下文预算里会变成每步必调的热路径。
func BenchmarkEstimateTextShort(b *testing.B) {
	s := "你好世界"
	for b.Loop() {
		EstimateText(s)
	}
}

func BenchmarkEstimateTextLong(b *testing.B) {
	s := strings.Repeat("这是一段中等长度的中文文本，用来模拟真实的对话内容。", 50)
	for b.Loop() {
		EstimateText(s)
	}
}

func BenchmarkEstimateTokensConversation(b *testing.B) {
	// 约 50 轮对话的历史，模拟中长会话
	msgs := make([]Message, 0, 50)
	for range 50 {
		msgs = append(msgs, NewText(RoleUser, "这是用户的一轮问题，包含一些背景信息"))
		msgs = append(msgs, NewText(RoleAssistant, "这是助手的回答，通常比问题长一些，包含解释和结论"))
	}
	req := Request{System: "你是白泽，一个个人 AI 助理。", Messages: msgs}

	for b.Loop() {
		EstimateTokens(req)
	}
}

func BenchmarkEstimateBlocksWithToolResult(b *testing.B) {
	blocks := []Block{
		NewTextBlock("前缀"),
		NewToolResultBlock("id", []Block{
			NewTextBlock(strings.Repeat("工具返回的大段内容", 40)),
		}, false),
		NewTextBlock("后缀"),
	}
	for b.Loop() {
		EstimateBlocks(blocks)
	}
}

func BenchmarkParseToolInputValid(b *testing.B) {
	raw := `{"city":"北京","date":"2026-11-07","options":{"detail":true}}`
	for b.Loop() {
		ParseToolInput(raw)
	}
}

func BenchmarkParseToolInputBroken(b *testing.B) {
	raw := `{"city":"北京","date":`
	for b.Loop() {
		ParseToolInput(raw)
	}
}

func BenchmarkTextOfBlocks(b *testing.B) {
	blocks := make([]Block, 0, 20)
	for range 10 {
		blocks = append(blocks, NewTextBlock("一段文本"))
		blocks = append(blocks, NewThinkingBlock("思考", ""))
	}
	for b.Loop() {
		TextOfBlocks(blocks)
	}
}

func BenchmarkMarshalBlocksUncached(b *testing.B) {
	// 用于评估历史缓存的收益：无缓存时每轮都要解码
	blocks := []Block{
		NewTextBlock("用户问题"),
		NewThinkingBlock("思考过程", "sig"),
		NewTextBlock("助手回答"),
	}
	data, _ := json.Marshal(blocks)

	for b.Loop() {
		_, _ = json.Marshal(blocks)
		_ = data
	}
}
