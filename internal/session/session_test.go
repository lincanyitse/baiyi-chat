package session

import (
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

func TestBlockRoundTrip(t *testing.T) {
	cases := [][]llm.Block{
		{llm.NewTextBlock("纯文本")},
		{llm.NewThinkingBlock("思考", "sig")},
		{llm.NewToolUseBlock("id1", "tool", []byte(`{"a":1}`))},
		{llm.NewToolResultBlock("id1", []llm.Block{llm.NewTextBlock("结果")}, false)},
		{
			llm.NewThinkingBlock("推理", "s"),
			llm.NewTextBlock("回答"),
		},
	}

	for i, blocks := range cases {
		data, err := MarshalBlocks(blocks)
		if err != nil {
			t.Fatalf("case %d marshal: %v", i, err)
		}
		got, err := UnmarshalBlocks(data)
		if err != nil {
			t.Fatalf("case %d unmarshal: %v", i, err)
		}
		if len(got) != len(blocks) {
			t.Fatalf("case %d: 块数 %d != %d", i, len(got), len(blocks))
		}
		for j := range blocks {
			if got[j].BlockType() != blocks[j].BlockType() {
				t.Errorf("case %d 块 %d: 类型 %q != %q", i, j, got[j].BlockType(), blocks[j].BlockType())
			}
		}
	}
}

func TestUnmarshalBlocksRejectsUnknownType(t *testing.T) {
	_, err := UnmarshalBlocks(`[{"type":"nonexistent"}]`)
	if err == nil {
		t.Fatal("未知块类型应返回错误")
	}
}

func TestTextOf(t *testing.T) {
	m := llm.Message{Content: []llm.Block{
		llm.NewTextBlock("a"),
		llm.NewThinkingBlock("b", ""),
		llm.NewTextBlock("c"),
	}}
	if got := llm.TextOf(m); got != "ac" {
		t.Errorf("TextOf = %q, 期望 ac", got)
	}
}

func TestTruncateTitle(t *testing.T) {
	short := "短标题"
	if truncateTitle(short) != short {
		t.Errorf("短标题不应被截断")
	}
	long := "这是一个非常非常非常非常非常非常长的用户输入内容需要被截断"
	got := truncateTitle(long)
	if len([]rune(got)) > 26 {
		t.Errorf("标题过长: %d 字", len([]rune(got)))
	}
}

func TestTurnStatusValues(t *testing.T) {
	statuses := []TurnStatus{TurnReceived, TurnRunning, TurnAwaiting, TurnCanceled, TurnFailed, TurnDone}
	seen := map[TurnStatus]bool{}
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("重复的状态值: %s", s)
		}
		seen[s] = true
		if s == "" {
			t.Error("状态不应为空")
		}
	}
}
