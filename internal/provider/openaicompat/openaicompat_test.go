package openaicompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 真实录制的一次 ModSwitch / GLM 流式响应（含 usage 在末尾空 choices 分片）。
const recordedSimple = `data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}],"id":"chatcmpl_1","object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"content":"你好"},"index":0}],"id":"chatcmpl_1","object":"chat.completion.chunk"}

data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"id":"chatcmpl_1","object":"chat.completion.chunk","usage":{"completion_tokens":3,"prompt_tokens":17,"total_tokens":20}}

data: [DONE]

`

// 工具调用分片：首片带 id/name 与部分参数，后续片只有参数。
const recordedToolFanout = `data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}],"id":"c2"}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]},"index":0}],"id":"c2"}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"北京\"}"}}]},"index":0}],"id":"c2"}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"id":"c2","usage":{"completion_tokens":10,"prompt_tokens":50}}

data: [DONE]

`

// 部分厂商每片重发 id/name，且 usage 缺失。
const recordedRepeatedID = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"ping","arguments":""}}]},"index":0}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"ping","arguments":"{}"}}]},"index":0}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}

data: [DONE]

`

// 残缺 JSON：必须不 panic，且以 parse_error 形式交付。
const recordedBrokenJSON = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","type":"function","function":{"name":"bad","arguments":"{\"a\":"}}]},"index":0}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}

data: [DONE]

`

func runStream(t *testing.T, payload string) []llm.Event {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
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

func TestStreamSimpleText(t *testing.T) {
	events := runStream(t, recordedSimple)

	var text string
	var usage *llm.UsageEvent
	var stop llm.StopReason
	for _, ev := range events {
		switch e := ev.(type) {
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
	if text != "你好" {
		t.Errorf("text = %q, 期望 你好", text)
	}
	if usage == nil || usage.Input != 17 || usage.Output != 3 {
		t.Errorf("usage = %+v, 期望 in=17 out=3", usage)
	}
	if stop != llm.StopEnd {
		t.Errorf("stop = %q, 期望 end", stop)
	}
}

func TestStreamToolCallFanout(t *testing.T) {
	events := runStream(t, recordedToolFanout)

	var ready []llm.ToolCallReadyEvent
	var stop llm.StopReason
	for _, ev := range events {
		switch e := ev.(type) {
		case llm.ToolCallReadyEvent:
			ready = append(ready, e)
		case llm.StopEvent:
			stop = e.Reason
		}
	}
	if len(ready) != 1 {
		t.Fatalf("期望 1 个 ToolCallReady，实际 %d", len(ready))
	}
	tc := ready[0]
	if tc.ID != "call_a" || tc.Name != "get_weather" {
		t.Errorf("id/name = %q/%q", tc.ID, tc.Name)
	}
	if string(tc.Input) != `{"city":"北京"}` {
		t.Errorf("拼装后的参数 = %s", tc.Input)
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q, 期望 tool_use", stop)
	}
}

func TestStreamRepeatedIDAndMissingUsage(t *testing.T) {
	events := runStream(t, recordedRepeatedID)

	var ready int
	var hasUsage bool
	for _, ev := range events {
		switch ev.(type) {
		case llm.ToolCallReadyEvent:
			ready++
		case llm.UsageEvent:
			hasUsage = true
		}
	}
	if ready != 1 {
		t.Errorf("期望 1 个 ToolCallReady，实际 %d", ready)
	}
	if hasUsage {
		t.Error("该录制流不含 usage，不应发出 UsageEvent")
	}
}

func TestStreamBrokenJSONDoesNotPanic(t *testing.T) {
	events := runStream(t, recordedBrokenJSON)

	var ready *llm.ToolCallReadyEvent
	for _, ev := range events {
		if e, ok := ev.(llm.ToolCallReadyEvent); ok {
			ready = &e
		}
	}
	if ready == nil {
		t.Fatal("残缺 JSON 也应交付 ToolCallReady（带 ParseErr）")
	}
	if ready.ParseErr == "" {
		t.Errorf("残缺 JSON 应设置 ParseErr，实际 Input=%s", ready.Input)
	}
	if ready.Input != nil {
		t.Errorf("ParseErr 非空时 Input 应为 nil，实际 %s", ready.Input)
	}
}

func TestStreamEventContract(t *testing.T) {
	events := runStream(t, recordedSimple)
	if len(events) < 2 {
		t.Fatal("事件不足")
	}
	if _, ok := events[0].(llm.StartEvent); !ok {
		t.Errorf("首事件应为 StartEvent，实际 %T", events[0])
	}
	last := events[len(events)-1]
	if _, ok := last.(llm.StopEvent); !ok {
		t.Errorf("末事件应为 StopEvent，实际 %T", last)
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   llm.ErrorKind
	}{
		{401, `{"error":{"message":"bad key"}}`, llm.ErrAuth},
		{402, `{"error":{"message":"insufficient balance"}}`, llm.ErrQuota},
		{429, `{"error":{"message":"too many requests"}}`, llm.ErrRateLimited},
		{400, `{"error":{"message":"bad request"}}`, llm.ErrInvalidRequest},
		{500, `{"error":{"message":"server error"}}`, llm.ErrTransient},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		p := New(Options{Name: "t", BaseURL: srv.URL})
		_, err := p.Stream(context.Background(), llm.Request{Model: "m"})
		srv.Close()
		if err == nil {
			t.Errorf("status %d 应返回错误", c.status)
			continue
		}
		e := llm.AsError(err)
		if e.Kind != c.want {
			t.Errorf("status %d: kind = %q, 期望 %q", c.status, e.Kind, c.want)
		}
	}
}

func TestEstimateTokensNonZero(t *testing.T) {
	n := llm.EstimateTokens(llm.Request{
		System:   "你是白泽。",
		Messages: []llm.Message{llm.NewText(llm.RoleUser, "介绍一下你自己")},
	})
	if n <= 0 {
		t.Errorf("估算 token 应大于 0，实际 %d", n)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("路径 = %q, 期望 /v1/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("鉴权头 = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[
			{"id":"model-a","object":"model","created":1,"owned_by":"gw"},
			{"id":"provider/model-b","object":"model","created":2,"owned_by":"gw"}
		]}`)
	}))
	defer srv.Close()

	p := New(Options{Name: "t", BaseURL: srv.URL + "/v1", APIKey: "k"})
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("模型数 = %d, 期望 2", len(models))
	}
	if models[0].ID != "model-a" || models[1].ID != "provider/model-b" {
		t.Errorf("模型 ID 不正确: %+v", models)
	}
}

func TestListModelsHandlesChatCompletionsBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	p := New(Options{Name: "t", BaseURL: srv.URL + "/v1/chat/completions"})
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("base_url 含 /chat/completions 时路径 = %q, 期望 /v1/models", gotPath)
	}
}

func TestListModelsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := New(Options{Name: "t", BaseURL: srv.URL + "/v1"})
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatal("401 应返回错误")
	}
	if llm.AsError(err).Kind != llm.ErrAuth {
		t.Errorf("kind = %q, 期望 auth", llm.AsError(err).Kind)
	}
}
