package responses

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 真实录制的 ModSwitch Responses 流（思考摘要 + 文本 + usage）。
const recordedText = `event: response.created
data: {"response":{"id":"resp_1","model":"m","object":"response","output":[],"status":"in_progress"},"type":"response.created"}

event: response.output_item.added
data: {"item":{"id":"rs_1","summary":[],"type":"reasoning"},"type":"response.output_item.added"}

event: response.reasoning_summary_text.delta
data: {"delta":"The user","item_id":"rs_1","type":"response.reasoning_summary_text.delta"}

event: response.output_item.done
data: {"item":{"id":"rs_1","type":"reasoning"},"type":"response.output_item.done"}

event: response.output_item.added
data: {"item":{"id":"msg_1","type":"message","role":"assistant"},"type":"response.output_item.added"}

event: response.output_text.delta
data: {"delta":"你","item_id":"msg_1","type":"response.output_text.delta"}

event: response.output_text.delta
data: {"delta":"好","item_id":"msg_1","type":"response.output_text.delta"}

event: response.output_item.done
data: {"item":{"id":"msg_1","type":"message"},"type":"response.output_item.done"}

event: response.completed
data: {"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":16,"output_tokens":20}},"type":"response.completed"}

`

// 工具调用：function_call item + arguments 增量。
const recordedToolCall = `event: response.created
data: {"type":"response.created"}

event: response.output_item.added
data: {"item":{"arguments":"","call_id":"call_9","id":"fc_1","name":"get_time","status":"in_progress","type":"function_call"},"type":"response.output_item.added"}

event: response.function_call_arguments.delta
data: {"delta":"{\"tz\":","item_id":"fc_1","type":"response.function_call_arguments.delta"}

event: response.function_call_arguments.delta
data: {"delta":"\"UTC\"}","item_id":"fc_1","type":"response.function_call_arguments.delta"}

event: response.output_item.done
data: {"item":{"arguments":"{\"tz\":\"UTC\"}","call_id":"call_9","id":"fc_1","name":"get_time","status":"completed","type":"function_call"},"type":"response.output_item.done"}

event: response.completed
data: {"response":{"status":"completed","usage":{"input_tokens":158,"output_tokens":6}},"type":"response.completed"}

`

const recordedFailed = `event: response.created
data: {"type":"response.created"}

event: response.failed
data: {"response":{"status":"failed","error":{"code":"server_error","message":"upstream exploded"}},"type":"response.failed"}

`

func runStream(t *testing.T, payload string) []llm.Event {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/responses") {
			t.Errorf("路径 = %q, 期望 /v1/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, payload)
	}))
	defer srv.Close()

	p := New(Options{Name: "t", BaseURL: srv.URL + "/v1", APIKey: "k"})
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

func TestStreamTextAndUsage(t *testing.T) {
	events := runStream(t, recordedText)

	var text, thinking string
	var usage *llm.UsageEvent
	var stop llm.StopReason
	for _, ev := range events {
		switch e := ev.(type) {
		case llm.TextDeltaEvent:
			text += e.Text
		case llm.ThinkingDeltaEvent:
			thinking += e.Text
		case llm.UsageEvent:
			u := e
			usage = &u
		case llm.StopEvent:
			stop = e.Reason
		case llm.ErrorEvent:
			t.Fatalf("意外错误: %v", e.Err)
		}
	}
	if thinking != "The user" {
		t.Errorf("thinking = %q", thinking)
	}
	if text != "你好" {
		t.Errorf("text = %q", text)
	}
	if usage == nil || usage.Input != 16 || usage.Output != 20 {
		t.Errorf("usage = %+v", usage)
	}
	if stop != llm.StopEnd {
		t.Errorf("stop = %q", stop)
	}
}

func TestStreamFunctionCall(t *testing.T) {
	events := runStream(t, recordedToolCall)

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
	if ready.ID != "call_9" || ready.Name != "get_time" {
		t.Errorf("id/name = %q/%q", ready.ID, ready.Name)
	}
	if string(ready.Input) != `{"tz":"UTC"}` {
		t.Errorf("参数 = %s", ready.Input)
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q, 期望 tool_use", stop)
	}
}

func TestStreamFailedEvent(t *testing.T) {
	events := runStream(t, recordedFailed)

	var gotErr *llm.Error
	for _, ev := range events {
		if e, ok := ev.(llm.ErrorEvent); ok {
			gotErr = e.Err
		}
	}
	if gotErr == nil {
		t.Fatal("response.failed 应转成 ErrorEvent")
	}
	if gotErr.Message != "upstream exploded" {
		t.Errorf("message = %q", gotErr.Message)
	}
	if gotErr.VendorCode != "server_error" {
		t.Errorf("vendor code = %q", gotErr.VendorCode)
	}
}

func TestEventContract(t *testing.T) {
	events := runStream(t, recordedText)
	if _, ok := events[0].(llm.StartEvent); !ok {
		t.Errorf("首事件应为 StartEvent，实际 %T", events[0])
	}
	last := events[len(events)-1]
	if _, ok := last.(llm.StopEvent); !ok {
		t.Errorf("末事件应为 StopEvent，实际 %T", last)
	}
}

func TestConvertInput(t *testing.T) {
	p := New(Options{Name: "t", BaseURL: "http://x"})
	input := p.convertInput(llm.Request{
		Messages: []llm.Message{
			llm.NewText(llm.RoleUser, "问题"),
			{Role: llm.RoleAssistant, Content: []llm.Block{
				llm.NewToolUseBlock("call_1", "tool", []byte(`{"a":1}`)),
			}},
			{Role: llm.RoleUser, Content: []llm.Block{
				llm.NewToolResultBlock("call_1", []llm.Block{llm.NewTextBlock("结果")}, false),
			}},
		},
	})

	if len(input) != 3 {
		t.Fatalf("input 项数 = %d, 期望 3", len(input))
	}
	if input[0]["type"] != "message" {
		t.Errorf("首项 type = %v", input[0]["type"])
	}
	if input[1]["type"] != "function_call" {
		t.Errorf("工具调用项 type = %v", input[1]["type"])
	}
	if input[1]["call_id"] != "call_1" {
		t.Errorf("call_id = %v", input[1]["call_id"])
	}
	if input[2]["type"] != "function_call_output" {
		t.Errorf("工具结果项 type = %v", input[2]["type"])
	}
}

func TestEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"http://x/v1":           "http://x/v1/responses",
		"http://x/v1/":          "http://x/v1/responses",
		"http://x/v1/responses": "http://x/v1/responses",
		"http://x":              "http://x/v1/responses",
	}
	for base, want := range cases {
		p := New(Options{Name: "t", BaseURL: base})
		if got := p.endpoint(); got != want {
			t.Errorf("base %q → %q, 期望 %q", base, got, want)
		}
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := New(Options{Name: "t", BaseURL: srv.URL + "/v1"})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m"})
	if err == nil {
		t.Fatal("401 应返回错误")
	}
	if llm.AsError(err).Kind != llm.ErrAuth {
		t.Errorf("kind = %q", llm.AsError(err).Kind)
	}
}
