package tool

import (
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 截断阈值。同时按 token 与字节限制，任一超限即截断。
const (
	DefaultMaxResultTokens = 8000
	DefaultMaxResultBytes  = 32 * 1024
)

// TruncateOptions 控制截断行为。
type TruncateOptions struct {
	MaxTokens int
	MaxBytes  int
}

// Truncated 是截断结果。
type Truncated struct {
	// Text 是保留的部分（含提示语）。
	Text string
	// Full 是未截断的原文，需要落盘时用。
	Full string
	// DidTruncate 表示是否发生了截断。
	DidTruncate bool
}

// TruncateResult 按 token 与字节双上限截断工具输出。
//
// 截断时保留头部并在末尾提示可用 tool_output_read 取回完整内容。
// 这是上下文预算降级的关键配套（架构纲要 §5.3 第 3 步）。
func TruncateResult(content string, opts TruncateOptions) Truncated {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = DefaultMaxResultTokens
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxResultBytes
	}

	est := llm.EstimateText(content)
	if est <= opts.MaxTokens && len(content) <= opts.MaxBytes {
		return Truncated{Text: content, Full: content}
	}

	// 按字节上限先切一刀，再按 token 上限收紧。
	cut := content
	if len(cut) > opts.MaxBytes {
		cut = safeCutUTF8(content, opts.MaxBytes)
	}

	// token 上限：按比例估算需要保留多少字节。
	if e := llm.EstimateText(cut); e > opts.MaxTokens {
		ratio := float64(opts.MaxTokens) / float64(e)
		keep := int(float64(len(cut)) * ratio * 0.95)
		if keep < 64 {
			keep = 64
		}
		cut = safeCutUTF8(cut, keep)
	}

	notice := fmt.Sprintf(
		"\n\n[内容已截断：完整结果 %d 字节，可用 tool_output_read 取回。]",
		len(content))
	return Truncated{
		Text:        cut + notice,
		Full:        content,
		DidTruncate: true,
	}
}

// safeCutUTF8 在不超过 n 字节处截断，且不切断多字节字符。
func safeCutUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && n < len(s) && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ApplyTruncation 把截断后的文本包成工具结果。
//
// 发生截断时调用 store 落盘完整内容，并在结果文本里带上句柄。
func ApplyTruncation(tr Truncated, store OutputStore, handle string) Result {
	text := tr.Text
	if tr.DidTruncate && handle != "" {
		text = strings.Replace(text,
			"可用 tool_output_read 取回。",
			"句柄 "+handle+"，可用 tool_output_read 取回。", 1)
	}
	return TextResult(text)
}
