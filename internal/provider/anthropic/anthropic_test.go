package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 真实录制的 ModSwitch Anthropic 协议流（含 thinking + signature）。
const recordedThinking = `event: message_start
data: {"message":{"content":[],"id":"msg_1","model":"m","role":"assistant","stop_reason":null,"type":"message","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":17,"output_tokens":0}},"type":"message_start"}

event: content_block_start
data: {"content_block":{"thinking":"","type":"thinking"},"index":0,"type":"content_block_start"}

event: content_block_delta
data: {"delta":{"thinking":"Let me think","type":"thinking_delta"},"index":0,"type":"content_block_delta"}

event: content_block_delta
data: {"delta":{"signature":"modswitch","type":"signature_delta"},"index":0,"type":"content_block_delta"}

event: content_block_stop
data: {"index":0,"type":"content_block_stop"}

event: content_block_start
data: {"content_block":{"text":"","type":"text"},"index":1,"type":"content_block_start"}

event: content_block_delta
data: {"delta":{"text":"OK","type":"text_delta"},"index":1,"type":"content_block_delta"}

event: content_block_stop
data: {"index":1,"type":"content_block_stop"}

event: message_delta
data: {"delta":{"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta","usage":{"input_tokens":17,"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

// 工具调用：content_block_start 带 id/name，input_json_delta 为分片。
const recordedToolUse = `event: message_start
data: {"message":{"usage":{"input_tokens":158,"output_tokens":0}},"type":"message_start"}

event: content_block_start
data: {"content_block":{"id":"call_1","input":{},"name":"get_time","type":"tool_use"},"index":0,"type":"content_block_start"}

event: content_block_delta
data: {"delta":{"partial_json":"{\"tz\":","type":"input_json_delta"},"index":0,"type":"content_block_delta"}

event: content_block_delta
data: {"delta":{"partial_json":"\"UTC\"}","type":"input_json_delta"},"index":0,"type":"content_block_delta"}

event: content_block_stop
data: {"index":0,"type":"content_block_stop"}

event: message_delta
data: {"delta":{"stop_reason":"tool_use"},"type":"message_delta","usage":{"input_tokens":158,"output_tokens":6}}

event: message_stop
data: {"type":"message_stop"}

`

// 中途错误事件。
const recordedMidStreamError = `event: message_start
data: {"message":{"usage":{"input_tokens":10,"output_tokens":0}},"type":"message_start"}

event: content_block_start
data: {"content_block":{"text":"","type":"text"},"index":0,"type":"content_block_start"}

event: content_block_delta
data: {"delta":{"text":"部分","type":"text_delta"},"index":0,"type":"content_block_delta"}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`

func runStream(t *testing.T, payload string) []llm.Event {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 校验关键请求头与路径
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("请求路径 = %q, 期望以 /v1/messages 结尾", r.URL.Path)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("缺少 anthropic-version 头")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, payload)
	}))
	defer srv.Close()

	p := New(Options{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.NewText(llm.RoleUser, "hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var events []llm.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestStreamThinkingAndText(t *testing.T) {
	events := runStream(t, recordedThinking)

	var thinking, text string
	var usage *llm.UsageEvent
	var stop llm.StopReason
	for _, ev := range events {
		switch e := ev.(type) {
		case llm.ThinkingDeltaEvent:
			thinking += e.Text
		case llm.TextDeltaEvent:
			text += e.Text
		case llm.UsageEvent:
			u := e
			usage = &u
		case llm.StopEvent:
			stop = e.Reason
		case llm.ErrorEvent:
			t.Fatalf("意外错误: %v", e.Err)
		}
	}
	if thinking != "Let me think" {
		t.Errorf("thinking = %q", thinking)
	}
	if text != "OK" {
		t.Errorf("text = %q", text)
	}
	if usage == nil || usage.Input != 17 || usage.Output != 3 {
		t.Errorf("usage = %+v", usage)
	}
	if stop != llm.StopEnd {
		t.Errorf("stop = %q", stop)
	}
}

func TestStreamToolUse(t *testing.T) {
	events := runStream(t, recordedToolUse)

	var ready *llm.ToolCallReadyEvent
	var stop llm.StopReason
	for _, ev := range events {
		switch e := ev.(type) {
		case llm.ToolCallReadyEvent:
			ready = &e
		case llm.StopEvent:
			stop = e.Reason
		}
	}
	if ready == nil {
		t.Fatal("未收到 ToolCallReady")
	}
	if ready.ID != "call_1" || ready.Name != "get_time" {
		t.Errorf("id/name = %q/%q", ready.ID, ready.Name)
	}
	if string(ready.Input) != `{"tz":"UTC"}` {
		t.Errorf("参数 = %s", ready.Input)
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q", stop)
	}
}

func TestStreamMidStreamError(t *testing.T) {
	events := runStream(t, recordedMidStreamError)

	var text string
	var gotErr *llm.Error
	for _, ev := range events {
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			text += e.Text
		case llm.ErrorEvent:
			gotErr = e.Err
		}
	}
	if text != "部分" {
		t.Errorf("错误前已输出的文本应保留，实际 %q", text)
	}
	if gotErr == nil || gotErr.Kind != llm.ErrTransient {
		t.Errorf("overloaded_error 应映射为 transient，实际 %+v", gotErr)
	}
}

func TestNoStopAfterError(t *testing.T) {
	events := runStream(t, recordedMidStreamError)
	for i, ev := range events {
		if _, ok := ev.(llm.ErrorEvent); ok {
			for _, after := range events[i+1:] {
				if _, isStop := after.(llm.StopEvent); isStop {
					t.Error("ErrorEvent 之后不应再有 StopEvent")
				}
			}
		}
	}
}

func TestMergeAdjacentSameRole(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "a"}}},
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "b"}}},
		{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "c"}}},
	}
	got, sets := mergeAdjacentSameRole(msgs, []int{0, 1, 2})
	if len(got) != 2 {
		t.Fatalf("期望合并为 2 条，实际 %d", len(got))
	}
	parts := got[0]["content"].([]map[string]any)
	if len(parts) != 2 {
		t.Errorf("合并后 user 消息应含 2 个块，实际 %d", len(parts))
	}
	// 合并后第一条应记录原始下标 0 与 1
	if len(sets[0]) != 2 || sets[0][0] != 0 || sets[0][1] != 1 {
		t.Errorf("原始下标映射 = %v, 期望 [0 1]", sets[0])
	}
}

func TestMessagesDropEmptyText(t *testing.T) {
	p := New(Options{Name: "t", BaseURL: "http://x"})
	msgs := p.convertMessages(llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.Block{llm.NewTextBlock(""), llm.NewTextBlock("有效")}},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d", len(msgs))
	}
	parts := msgs[0]["content"].([]map[string]any)
	if len(parts) != 1 {
		t.Errorf("空文本块应被丢弃，实际块数 %d", len(parts))
	}
}
