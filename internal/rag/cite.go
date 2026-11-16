package rag

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// CitationBlock 把检索结果渲染成模型可读的带编号引用。
//
// 编号与 SearchResult.Chunks 的下标一致，模型据此在回答里写 [1][2]，
// 前端再用 Citation 的偏移去原文定位。
func CitationBlock(res SearchResult) string {
	if len(res.Chunks) == 0 {
		if res.Degraded {
			return "知识库中没有找到相关内容。（" + res.Reason + "）"
		}
		return "知识库中没有找到相关内容。"
	}

	var b strings.Builder
	if res.Degraded {
		fmt.Fprintf(&b, "（%s）\n\n", res.Reason)
	}

	for i, c := range res.Chunks {
		head := c.Title
		if head == "" {
			head = "未命名文档"
		}
		if c.HeadingPath != "" && c.HeadingPath != c.Title {
			head += " / " + c.HeadingPath
		}
		fmt.Fprintf(&b, "[%d] %s", i+1, head)
		if c.Page > 0 {
			fmt.Fprintf(&b, "（第 %d 页）", c.Page)
		}
		if c.VectorScore >= 0 {
			fmt.Fprintf(&b, " 相关度 %.2f", c.VectorScore)
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(c.Snippet))
		b.WriteString("\n\n")
	}

	b.WriteString("引用时请标注对应的编号，例如 [1]。")
	return b.String()
}

// CitationBlocks 把引用转成 llm.CitationBlock，随回答一起下发。
func CitationBlocks(res SearchResult) []llm.Block {
	if len(res.Chunks) == 0 {
		return nil
	}
	out := make([]llm.Block, 0, len(res.Chunks))
	for i, c := range res.Chunks {
		out = append(out, llm.CitationBlock{
			Type:        string(llm.BlockCitation),
			Index:       i + 1,
			DocID:       c.DocumentID,
			ChunkID:     c.ChunkID,
			Title:       c.Title,
			HeadingPath: c.HeadingPath,
			Page:        c.Page,
			Start:       c.CharStart,
			End:         c.CharEnd,
			Snippet:     textutil.TruncateRunes(c.Snippet, 200),
			URI:         c.Source,
		})
	}
	return out
}

// SearchToolInput 是 rag_search 工具的入参。
type SearchToolInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// ParseSearchInput 解析工具入参。
func ParseSearchInput(raw json.RawMessage) (SearchToolInput, error) {
	var in SearchToolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return SearchToolInput{}, fmt.Errorf("参数格式错误: %w", err)
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return SearchToolInput{}, fmt.Errorf("query 不能为空")
	}
	if in.Limit <= 0 {
		in.Limit = 8
	}
	if in.Limit > 20 {
		in.Limit = 20
	}
	return in, nil
}
