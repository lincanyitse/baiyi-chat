package web

import (
	"encoding/json"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 引用块此前被 toMessageDTO 静默丢弃，回答里的 [1] 变成死文字。
func TestToMessageDTOKeepsCitations(t *testing.T) {
	m := llm.Message{
		ID:   "m1",
		Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.NewTextBlock("这是结论 [1]。"),
			llm.CitationBlock{
				Type:        string(llm.BlockCitation),
				Index:       1,
				DocID:       "doc-1",
				ChunkID:     "chunk-3",
				Start:       12,
				End:         48,
				Title:       "架构纲要",
				HeadingPath: "§2 分层",
				Page:        7,
				Snippet:     "分层方向由脚本强制",
				URI:         "kb://doc-1",
			},
		},
	}

	got := toMessageDTO(m)
	if len(got.Content) != 2 {
		t.Fatalf("应保留两个内容块，实际 %d", len(got.Content))
	}

	c := got.Content[1]
	if c.Type != string(llm.BlockCitation) {
		t.Errorf("类型应为 citation，实际 %q", c.Type)
	}
	if c.Index != 1 || c.DocID != "doc-1" || c.ChunkID != "chunk-3" {
		t.Errorf("定位字段不完整: %+v", c)
	}
	if c.Start != 12 || c.End != 48 {
		t.Errorf("偏移丢失: %+v", c)
	}
	if c.Title != "架构纲要" || c.HeadingPath != "§2 分层" || c.Page != 7 {
		t.Errorf("展示字段不完整: %+v", c)
	}
	if c.Snippet == "" || c.URI == "" {
		t.Error("snippet / uri 不该丢")
	}

	// 序列化后字段名的下划线形式要正确，前端按这个读
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"doc_id", "chunk_id", "heading_path", "snippet"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("序列化后缺字段 %s: %s", key, raw)
		}
	}
}

// tool_result 已经有转换，但前端一直没渲染。这里锁住它别再退化。
func TestToMessageDTOKeepsToolResult(t *testing.T) {
	m := llm.Message{
		ID:   "m1",
		Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.NewToolUseBlock("t1", "rag_search", json.RawMessage(`{"q":"x"}`)),
			llm.NewToolResultBlock("t1", []llm.Block{llm.NewTextBlock("找到 3 条")}, false),
		},
	}

	got := toMessageDTO(m)
	if len(got.Content) != 2 {
		t.Fatalf("应有 tool_use + tool_result，实际 %d 块", len(got.Content))
	}
	if got.Content[0].Type != string(llm.BlockToolUse) || got.Content[0].ID != "t1" {
		t.Errorf("tool_use 不对: %+v", got.Content[0])
	}
	res := got.Content[1]
	if res.Type != string(llm.BlockToolResult) || res.ID != "t1" {
		t.Errorf("tool_result 的关联 ID 不对: %+v", res)
	}
	if res.Text != "找到 3 条" || res.IsError {
		t.Errorf("结果内容不对: %+v", res)
	}
}
