package llm

import "encoding/json"

// StopReason 是模型停止生成的原因（归一化）。
type StopReason string

const (
	StopEnd      StopReason = "end"      // 正常结束
	StopToolUse  StopReason = "tool_use" // 需要执行工具
	StopLength   StopReason = "length"   // 达到 max_tokens
	StopFiltered StopReason = "filtered" // 内容审核拦截
	StopError    StopReason = "error"
)

// Event 是归一化后的流式事件。
//
// 契约（适配器必须满足，运行时可以依赖）：
//  1. BlockStart 必先于该 index 的任何 delta。
//  2. Index 单调递增且不复用。
//  3. Stop 或 Error 恰好一次，且是流的最后一条事件。
//  4. Stop 之后不得再有任何 delta。
//  5. ToolCallReady 只在参数 JSON 完整校验通过后发出。
//  6. Usage 可在 Stop 之前的任意位置出现（OpenAI 常在最后一个空 choices 分片带）。
type Event interface {
	EventType() EventType
}

// EventType 是事件类型标识。
type EventType string

const (
	EventStart         EventType = "start"
	EventBlockStart    EventType = "block_start"
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventToolArgsDelta EventType = "tool_args_delta"
	EventBlockStop     EventType = "block_stop"
	EventToolCallReady EventType = "tool_call_ready"
	EventUsage         EventType = "usage"
	EventStop          EventType = "stop"
	EventPing          EventType = "ping"
	EventError         EventType = "error"
)

// StartEvent 是流的第一条事件。
type StartEvent struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	RequestID string `json:"request_id,omitempty"`
}

func (StartEvent) EventType() EventType { return EventStart }

// BlockStartEvent 声明一个内容块开始。
type BlockStartEvent struct {
	Index int       `json:"index"`
	Type  BlockType `json:"type"`
}

func (BlockStartEvent) EventType() EventType { return EventBlockStart }

// TextDeltaEvent 是文本增量。
type TextDeltaEvent struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

func (TextDeltaEvent) EventType() EventType { return EventTextDelta }

// ThinkingDeltaEvent 是推理文本增量。
type ThinkingDeltaEvent struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

func (ThinkingDeltaEvent) EventType() EventType { return EventThinkingDelta }

// ToolArgsDeltaEvent 是工具参数 JSON 的字符串分片。
type ToolArgsDeltaEvent struct {
	Index       int    `json:"index"`
	PartialJSON string `json:"partial_json"`
}

func (ToolArgsDeltaEvent) EventType() EventType { return EventToolArgsDelta }

// BlockStopEvent 声明一个内容块结束。
type BlockStopEvent struct {
	Index int `json:"index"`
}

func (BlockStopEvent) EventType() EventType { return EventBlockStop }

// ToolCallReadyEvent 是一次工具调用解析完成的结果。
//
// 契约：Input 在 ParseErr 为空时保证是合法 JSON。参数残缺时 ParseErr 非空、
// Input 为 nil——运行时据此生成 tool_result(is_error=true) 让模型重发，
// 不要用魔法字段把解析错误塞进 Input。
type ToolCallReadyEvent struct {
	Index    int             `json:"index"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input,omitempty"`
	ParseErr string          `json:"parse_err,omitempty"`
}

// ParseToolInput 校验工具参数 JSON。
//
// 返回合法的参数与空的错误串；非法时返回 nil 与错误说明。
// 三个适配器共用同一判定，避免各自实现漂移。
func ParseToolInput(raw string) (json.RawMessage, string) {
	if raw == "" {
		return json.RawMessage("{}"), ""
	}
	if !json.Valid([]byte(raw)) {
		return nil, "工具参数不是合法 JSON"
	}
	return json.RawMessage(raw), ""
}

func (ToolCallReadyEvent) EventType() EventType { return EventToolCallReady }

// UsageEvent 是 token 用量。字段可为 0（部分厂商不返回）。
type UsageEvent struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Cached    int `json:"cached"`
	Reasoning int `json:"reasoning"`
}

func (UsageEvent) EventType() EventType { return EventUsage }

// StopEvent 是流的终止事件。
type StopEvent struct {
	Reason StopReason `json:"reason"`
}

func (StopEvent) EventType() EventType { return EventStop }

// PingEvent 是保活事件，消费方可忽略。
type PingEvent struct{}

func (PingEvent) EventType() EventType { return EventPing }

// ErrorEvent 是流中的错误终止。
type ErrorEvent struct {
	Err *Error `json:"error"`
}

func (ErrorEvent) EventType() EventType { return EventError }
