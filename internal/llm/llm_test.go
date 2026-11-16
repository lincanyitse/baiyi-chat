package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEstimateTextMonotonic(t *testing.T) {
	short := EstimateText("你好")
	long := EstimateText("你好，这是一个更长的句子用来验证估算随长度增长")
	if long <= short {
		t.Errorf("更长的文本应有更多 token: short=%d long=%d", short, long)
	}
}

func TestEstimateTextCJKHeavierThanASCII(t *testing.T) {
	// 相同字符数下，中文 token 数应高于英文（0.7 vs 0.25 token/字符）
	zh := EstimateText("一二三四五六七八")
	en := EstimateText("abcdefgh")
	if zh <= en {
		t.Errorf("同长度中文 token 应多于英文: zh=%d en=%d", zh, en)
	}
}

func TestEstimateTextEmpty(t *testing.T) {
	// 空文本返回固定结构开销 4，不应为负或 panic
	if got := EstimateText(""); got != 4 {
		t.Errorf("空文本 = %d, 期望 4", got)
	}
}

func TestEstimateTokensIncludesSystem(t *testing.T) {
	withSys := EstimateTokens(Request{
		System:   "你是一个助理",
		Messages: []Message{NewText(RoleUser, "hi")},
	})
	withoutSys := EstimateTokens(Request{
		Messages: []Message{NewText(RoleUser, "hi")},
	})
	if withSys <= withoutSys {
		t.Errorf("system 应计入: with=%d without=%d", withSys, withoutSys)
	}
}

func TestEstimateTokensPerMessageOverhead(t *testing.T) {
	one := EstimateTokens(Request{Messages: []Message{NewText(RoleUser, "x")}})
	two := EstimateTokens(Request{Messages: []Message{NewText(RoleUser, "x"), NewText(RoleAssistant, "y")}})
	if two <= one {
		t.Errorf("每条消息应有结构开销: one=%d two=%d", one, two)
	}
}

func TestEstimateBlocksCoversAllTypes(t *testing.T) {
	cases := []struct {
		name string
		blk  Block
		min  int
	}{
		{"text", NewTextBlock("你好世界"), 4},
		{"thinking", NewThinkingBlock("思考内容", ""), 4},
		{"tool_use", NewToolUseBlock("id", "tool", json.RawMessage(`{"a":1}`)), 4},
		{"tool_result", NewToolResultBlock("id", []Block{NewTextBlock("结果")}, false), 4},
		{"image", NewImageBlock(ImageSource{URL: "http://x"}, ""), 800},
		{"citation", CitationBlock{Type: string(BlockCitation), DocID: "d"}, 8},
		{"file", FileBlock{Type: string(BlockFile), Name: "a.txt"}, 4},
	}
	for _, c := range cases {
		got := EstimateBlocks([]Block{c.blk})
		if got < c.min {
			t.Errorf("%s: 估算 %d, 期望至少 %d", c.name, got, c.min)
		}
	}
}

func TestEstimateBlocksNestedToolResult(t *testing.T) {
	nested := NewToolResultBlock("id", []Block{
		NewTextBlock("这是一段比较长的工具结果内容用于验证递归估算"),
	}, false)
	empty := NewToolResultBlock("id", nil, false)

	if EstimateBlocks([]Block{nested}) <= EstimateBlocks([]Block{empty}) {
		t.Error("工具结果的内容应被递归计入")
	}
}

func TestTextOfBlocks(t *testing.T) {
	blocks := []Block{
		NewTextBlock("a"),
		NewThinkingBlock("b", ""),
		NewTextBlock("c"),
		NewImageBlock(ImageSource{URL: "x"}, ""),
	}
	if got := TextOfBlocks(blocks); got != "ac" {
		t.Errorf("TextOfBlocks = %q, 期望 ac", got)
	}
	if got := TextOf(Message{Content: blocks}); got != "ac" {
		t.Errorf("TextOf = %q, 期望 ac", got)
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]ErrorKind{
		400: ErrInvalidRequest,
		401: ErrAuth,
		403: ErrAuth,
		404: ErrInvalidRequest,
		402: ErrQuota,
		413: ErrInvalidRequest,
		422: ErrInvalidRequest,
		429: ErrRateLimited,
		500: ErrTransient,
		502: ErrTransient,
		503: ErrTransient,
		418: ErrUnknown,
	}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Errorf("status %d → %q, 期望 %q", status, got, want)
		}
	}
}

func TestErrorRetryable(t *testing.T) {
	yes := []ErrorKind{ErrTransient, ErrRateLimited}
	no := []ErrorKind{ErrAuth, ErrQuota, ErrInvalidRequest, ErrContextOverflow, ErrPolicy, ErrCanceled}

	for _, k := range yes {
		if !(&Error{Kind: k}).Retryable() {
			t.Errorf("%q 应可重试", k)
		}
	}
	for _, k := range no {
		if (&Error{Kind: k}).Retryable() {
			t.Errorf("%q 不应可重试", k)
		}
	}
}

func TestErrorFallbackable(t *testing.T) {
	yes := []ErrorKind{ErrTransient, ErrRateLimited, ErrQuota, ErrAuth, ErrNotSupported}
	no := []ErrorKind{ErrInvalidRequest, ErrPolicy, ErrCanceled, ErrContextOverflow}

	for _, k := range yes {
		if !(&Error{Kind: k}).Fallbackable() {
			t.Errorf("%q 应触发降级", k)
		}
	}
	for _, k := range no {
		if (&Error{Kind: k}).Fallbackable() {
			t.Errorf("%q 不应触发降级", k)
		}
	}
}

func TestAsErrorWrapsUnknown(t *testing.T) {
	plain := errString("boom")
	e := AsError(plain)
	if e.Kind != ErrUnknown {
		t.Errorf("普通错误应归为 unknown, 实际 %q", e.Kind)
	}
	if e.Message != "boom" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestAsErrorPreservesTyped(t *testing.T) {
	orig := &Error{Kind: ErrQuota, Message: "余额不足"}
	if got := AsError(orig); got != orig {
		t.Error("已是 *Error 时应原样返回")
	}
}

func TestErrorFormatting(t *testing.T) {
	withVendor := &Error{Kind: ErrQuota, HTTPStatus: 402, VendorCode: "11102", Message: "余额不足"}
	if s := withVendor.Error(); s == "" {
		t.Error("Error() 不应为空")
	}
	if !contains(withVendor.Error(), "11102") || !contains(withVendor.Error(), "余额不足") {
		t.Errorf("应包含厂商码与消息: %s", withVendor.Error())
	}

	plain := NewError(ErrAuth, 401, "unauthorized")
	if !contains(plain.Error(), "auth") {
		t.Errorf("应包含错误类别: %s", plain.Error())
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("短字符串不应截断: %q", got)
	}
	long := "0123456789abcdef"
	got := Truncate(long, 5)
	if len(got) > 8 {
		t.Errorf("截断后过长: %q", got)
	}
	if !contains(got, "...") {
		t.Errorf("截断应加省略号: %q", got)
	}
}

func TestParseToolInputValid(t *testing.T) {
	input, errStr := ParseToolInput(`{"a":1}`)
	if errStr != "" {
		t.Fatalf("合法 JSON 不应报错: %s", errStr)
	}
	if string(input) != `{"a":1}` {
		t.Errorf("input = %s", input)
	}
}

func TestParseToolInputEmpty(t *testing.T) {
	input, errStr := ParseToolInput("")
	if errStr != "" {
		t.Errorf("空参数应视为空对象，不应报错: %s", errStr)
	}
	if string(input) != "{}" {
		t.Errorf("空参数应返回 {}, 实际 %s", input)
	}
}

func TestParseToolInputBroken(t *testing.T) {
	input, errStr := ParseToolInput(`{"a":`)
	if errStr == "" {
		t.Fatal("残缺 JSON 应返回错误")
	}
	if input != nil {
		t.Errorf("出错时不应返回参数，实际 %s", input)
	}
}

func TestUsableBudget(t *testing.T) {
	caps := Capabilities{ContextWindow: 100000, MaxOutput: 8000}
	got := caps.Usable()
	// 100000 - 8000 - 10000 = 82000
	if got != 82000 {
		t.Errorf("Usable = %d, 期望 82000", got)
	}
}

func TestUsableBudgetFloor(t *testing.T) {
	caps := Capabilities{ContextWindow: 100, MaxOutput: 100}
	if got := caps.Usable(); got < 1024 {
		t.Errorf("预算不应低于下限 1024, 实际 %d", got)
	}
}

func TestCapabilityDefaults(t *testing.T) {
	oa := DefaultOpenAICompat()
	if oa.ContextWindow == 0 || !oa.Tools || oa.Thinking {
		t.Errorf("OpenAI 默认能力不合预期: %+v", oa)
	}

	an := DefaultAnthropic()
	if an.ContextWindow == 0 || !an.Thinking || !an.PrefixCache || !an.RequiresAlternate {
		t.Errorf("Anthropic 默认能力不合预期: %+v", an)
	}
	if an.ContextWindow <= oa.ContextWindow {
		t.Error("Anthropic 上下文窗口应大于 OpenAI 兼容默认值")
	}
}

func TestMessageConstructors(t *testing.T) {
	m := NewText(RoleUser, "hi")
	if m.Role != RoleUser || len(m.Content) != 1 {
		t.Fatalf("NewText 构造错误: %+v", m)
	}
	tb, ok := m.Content[0].(TextBlock)
	if !ok {
		t.Fatalf("内容块类型 = %T", m.Content[0])
	}
	if tb.Type != string(BlockText) {
		t.Errorf("Type 字段未设置: %q", tb.Type)
	}
}

func TestBlockConstructorsSetType(t *testing.T) {
	cases := []struct {
		blk  Block
		want BlockType
	}{
		{NewTextBlock("x"), BlockText},
		{NewThinkingBlock("x", "sig"), BlockThinking},
		{NewToolUseBlock("id", "n", nil), BlockToolUse},
		{NewToolResultBlock("id", nil, false), BlockToolResult},
		{NewImageBlock(ImageSource{}, ""), BlockImage},
	}
	for _, c := range cases {
		if c.blk.BlockType() != c.want {
			t.Errorf("%T: BlockType = %q, 期望 %q", c.blk, c.blk.BlockType(), c.want)
		}
	}
}

func TestEventTypes(t *testing.T) {
	events := []Event{
		StartEvent{}, BlockStartEvent{}, TextDeltaEvent{}, ThinkingDeltaEvent{},
		ToolArgsDeltaEvent{}, BlockStopEvent{}, ToolCallReadyEvent{},
		UsageEvent{}, StopEvent{}, PingEvent{}, ErrorEvent{},
	}
	seen := map[EventType]bool{}
	for _, ev := range events {
		typ := ev.EventType()
		if typ == "" {
			t.Errorf("%T 的事件类型为空", ev)
		}
		if seen[typ] {
			t.Errorf("事件类型重复: %q", typ)
		}
		seen[typ] = true
	}
}

func TestParseHTTPError(t *testing.T) {
	resp := fakeResponse(429, `{"error":{"message":"too many","type":"rate_limit"}}`,
		map[string]string{"Retry-After": "7"})

	defer resp.Body.Close()
	e := ParseHTTPError(resp)
	if e.Kind != ErrRateLimited {
		t.Errorf("kind = %q", e.Kind)
	}
	if e.HTTPStatus != 429 {
		t.Errorf("status = %d", e.HTTPStatus)
	}
	if e.Message != "too many" {
		t.Errorf("message = %q", e.Message)
	}
	if e.VendorCode != "rate_limit" {
		t.Errorf("vendor code = %q", e.VendorCode)
	}
	if e.RetryAfter != 7*time.Second {
		t.Errorf("retry after = %v", e.RetryAfter)
	}
	if len(e.Raw) == 0 {
		t.Error("应保留原始报文")
	}
}

func TestParseHTTPErrorVendorCodeFallback(t *testing.T) {
	// 没有 type 时用 code 作为厂商码
	resp := fakeResponse(400, `{"error":{"message":"bad","code":11102}}`, nil)
	defer resp.Body.Close()
	e := ParseHTTPError(resp)
	if e.VendorCode != "11102" {
		t.Errorf("vendor code = %q, 期望 11102", e.VendorCode)
	}
}

func TestParseHTTPErrorNonJSONBody(t *testing.T) {
	resp := fakeResponse(500, `upstream exploded`, nil)
	defer resp.Body.Close()
	e := ParseHTTPError(resp)
	if e.Kind != ErrTransient {
		t.Errorf("kind = %q", e.Kind)
	}
	if !contains(e.Message, "upstream exploded") {
		t.Errorf("非 JSON 响应体也应保留: %q", e.Message)
	}
}

func TestParseHTTPErrorLargeBodyTruncated(t *testing.T) {
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'x'
	}
	resp := fakeResponse(500, string(big), nil)
	defer resp.Body.Close()
	e := ParseHTTPError(resp)
	if len(e.Message) > 900 {
		t.Errorf("消息应被截断，实际 %d 字节", len(e.Message))
	}
	if len(e.Raw) > 64*1024 {
		t.Errorf("原始报文应限制在 64KB，实际 %d", len(e.Raw))
	}
}

func errString(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func fakeResponse(status int, body string, headers map[string]string) *http.Response {
	req := httptest.NewRequest(http.MethodPost, "http://example/test", nil)
	rec := httptest.NewRecorder()
	for k, v := range headers {
		rec.Header().Set(k, v)
	}
	rec.WriteHeader(status)
	rec.WriteString(body)
	resp := rec.Result()
	resp.Request = req
	return resp
}
