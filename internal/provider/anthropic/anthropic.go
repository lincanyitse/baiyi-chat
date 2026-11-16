// Package anthropic 实现 Anthropic Messages API 原生协议的流式适配器。
//
// 与 OpenAI 兼容的关键差异（全部在这一层吸收）：
//   - system 是顶层参数而非消息
//   - 工具调用在 assistant 消息的 tool_use 块内，结果在 user 消息的 tool_result 块内
//   - 流式是具名事件 + 按 index 的块生命周期
//   - thinking 块的 signature 必须原样带回
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/sse"
)

// Options 是适配器构造参数。
type Options struct {
	Name         string
	BaseURL      string // 例如 http://127.0.0.1:8899/code
	APIKey       string
	Version      string // anthropic-version，默认 2023-06-01
	Headers      map[string]string
	Client       *http.Client
	Capabilities func(model string) llm.Capabilities
	ExtraBody    map[string]any
}

// Provider 是 Anthropic 原生协议适配器。
type Provider struct {
	opts Options
}

// New 构造适配器。
func New(opts Options) *Provider {
	if opts.Client == nil {
		opts.Client = &http.Client{}
	}
	if opts.Version == "" {
		opts.Version = "2023-06-01"
	}
	if opts.Capabilities == nil {
		opts.Capabilities = func(string) llm.Capabilities { return llm.DefaultAnthropic() }
	}
	return &Provider{opts: opts}
}

func (p *Provider) Name() string { return p.opts.Name }

func (p *Provider) Capabilities(model string) llm.Capabilities { return p.opts.Capabilities(model) }

func (p *Provider) CountTokens(_ context.Context, req llm.Request) (int, error) {
	return llm.EstimateTokens(req), nil
}

// Stream 发起流式请求。
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", p.opts.Version)
	if p.opts.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.opts.APIKey)
		// 部分网关只认 Authorization，两个都带上。
		httpReq.Header.Set("Authorization", "Bearer "+p.opts.APIKey)
	}
	for k, v := range p.opts.Headers {
		httpReq.Header.Set(k, v)
	}

	// body 的两条路径都已关闭：非 200 在 parseHTTPError 前 defer；
	// 正常流在下方 goroutine 结束时 defer。linter 无法跨 goroutine 追踪。
	resp, err := p.opts.Client.Do(httpReq) //nolint:bodyclose
	if err != nil {
		return nil, llm.AsError(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseHTTPError(resp)
	}

	ch := make(chan llm.Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		p.pump(ctx, resp.Body, req.Model, ch)
	}()
	return ch, nil
}

func (p *Provider) endpoint() string {
	base := strings.TrimSuffix(p.opts.BaseURL, "/")
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

func (p *Provider) buildBody(req llm.Request) (io.Reader, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   p.convertMessages(req),
	}
	if req.System != "" {
		payload["system"] = req.System
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		payload["stop_sequences"] = req.Stop
	}
	if req.Thinking != nil && req.Thinking.Enabled {
		payload["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": req.Thinking.BudgetTokens,
		}
	}
	if len(req.Tools) > 0 {
		payload["tools"] = toAnthropicTools(req.Tools)
		if req.ToolChoice != "" {
			payload["tool_choice"] = toAnthropicToolChoice(req.ToolChoice)
		}
	}
	for k, v := range p.opts.ExtraBody {
		if _, exists := payload[k]; !exists {
			payload[k] = v
		}
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	return bytes.NewReader(b), nil
}

// convertMessages 把统一模型翻译为 Anthropic 消息数组。
// 关键：tool_result 必须放在 user 角色消息内；角色需严格交替。
func (p *Provider) convertMessages(req llm.Request) []map[string]any {
	var out []map[string]any
	// origIndex 记录每条输出消息对应的原始 req.Messages 下标，
	// 用于把 CacheBreakpoints 精确落到正确位置（转换中可能有消息被跳过）。
	origIndex := make([]int, 0, len(req.Messages))

	for i, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			continue // 已提升为顶层 system
		}

		var parts []map[string]any
		for _, blk := range m.Content {
			switch b := blk.(type) {
			case llm.TextBlock:
				if b.Text == "" {
					continue // 空文本块会 400
				}
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			case llm.ImageBlock:
				parts = append(parts, toImagePart(b))
			case llm.FileBlock:
				parts = append(parts, toDocumentPart(b))
			case llm.ThinkingBlock:
				tb := map[string]any{"type": "thinking", "thinking": b.Text}
				if b.Signature != "" {
					tb["signature"] = b.Signature
				}
				parts = append(parts, tb)
			case llm.ToolUseBlock:
				var input any = map[string]any{}
				if len(b.Input) > 0 {
					_ = json.Unmarshal(b.Input, &input)
				}
				parts = append(parts, map[string]any{
					"type":  "tool_use",
					"id":    b.ID,
					"name":  b.Name,
					"input": input,
				})
			case llm.ToolResultBlock:
				content := []map[string]any{}
				for _, inner := range b.Content {
					if t, ok := inner.(llm.TextBlock); ok && t.Text != "" {
						content = append(content, map[string]any{"type": "text", "text": t.Text})
					}
				}
				if len(content) == 0 {
					content = append(content, map[string]any{"type": "text", "text": "(empty result)"})
				}
				parts = append(parts, map[string]any{
					"type":        "tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     content,
					"is_error":    b.IsError,
				})
			}
		}

		if len(parts) == 0 {
			continue
		}

		role := string(m.Role)
		// 工具结果必须属于 user 角色
		for _, part := range parts {
			if part["type"] == "tool_result" {
				role = string(llm.RoleUser)
				break
			}
		}
		out = append(out, map[string]any{"role": role, "content": parts})
		origIndex = append(origIndex, i)
	}

	merged, mergedOrig := mergeAdjacentSameRole(out, origIndex)
	applyCacheBreakpoints(merged, mergedOrig, req.CacheBreakpoints)
	return merged
}

// mergeAdjacentSameRole 保证角色严格交替（Anthropic 硬要求）。
// 同时返回每条合并后消息所对应的原始消息下标集合，供缓存断点定位。
func mergeAdjacentSameRole(msgs []map[string]any, origIndex []int) ([]map[string]any, [][]int) {
	if len(msgs) < 2 {
		sets := make([][]int, len(msgs))
		for i := range msgs {
			sets[i] = []int{origIndex[i]}
		}
		return msgs, sets
	}
	out := make([]map[string]any, 0, len(msgs))
	sets := make([][]int, 0, len(msgs))

	for i, m := range msgs {
		if len(out) > 0 && out[len(out)-1]["role"] == m["role"] {
			prev := out[len(out)-1]
			prevParts, _ := prev["content"].([]map[string]any)
			curParts, _ := m["content"].([]map[string]any)
			prev["content"] = append(prevParts, curParts...)
			sets[len(sets)-1] = append(sets[len(sets)-1], origIndex[i])
			continue
		}
		out = append(out, m)
		sets = append(sets, []int{origIndex[i]})
	}
	return out, sets
}

// applyCacheBreakpoints 给断点位置最后一条消息的最后一个内容块打 cache_control。
//
// 断点是原始消息下标；合并后落在包含该下标的输出消息上。
// Anthropic 最多允许 4 个断点，超出部分从末尾截断（越靠后收益越高）。
func applyCacheBreakpoints(msgs []map[string]any, sets [][]int, breakpoints []int) {
	if len(breakpoints) == 0 {
		return
	}
	wanted := breakpoints
	if len(wanted) > maxCacheBreakpoints {
		wanted = wanted[len(wanted)-maxCacheBreakpoints:]
	}

	targets := make(map[int]bool, len(wanted))
	for _, orig := range wanted {
		for mi, set := range sets {
			for _, o := range set {
				if o == orig {
					targets[mi] = true
				}
			}
		}
	}

	for mi := range targets {
		parts, ok := msgs[mi]["content"].([]map[string]any)
		if !ok || len(parts) == 0 {
			continue
		}
		last := parts[len(parts)-1]
		last["cache_control"] = map[string]any{"type": "ephemeral"}
	}
}

// maxCacheBreakpoints 是 Anthropic 允许的最大缓存断点数。
const maxCacheBreakpoints = 4

func toImagePart(b llm.ImageBlock) map[string]any {
	if b.Source.URL != "" {
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": b.Source.URL},
		}
	}
	mt := b.Source.MediaType
	if mt == "" {
		mt = "image/png"
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mt,
			"data":       base64.StdEncoding.EncodeToString(b.Source.Data),
		},
	}
}

// toDocumentPart 把文件块转成 Anthropic 的 document 内容块。
//
// 只支持 PDF 与纯文本：Anthropic 的 document 类型接受这两种 media_type，
// 其他类型会在服务端被拒。调用方应在上传阶段就过滤掉。
func toDocumentPart(b llm.FileBlock) map[string]any {
	mt := b.MediaType
	if mt == "" {
		mt = "text/plain"
	}
	return map[string]any{
		"type": "document",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mt,
			"data":       base64.StdEncoding.EncodeToString(b.Data),
		},
	}
}

// pump 解析 Anthropic 具名事件流。
type blockAcc struct {
	kind      string
	text      strings.Builder
	signature string
	toolID    string
	toolName  string
	toolArgs  strings.Builder
}

func (p *Provider) pump(ctx context.Context, r io.Reader, model string, out chan<- llm.Event) {
	sc := sse.NewScanner(r)

	send := func(ev llm.Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !send(llm.StartEvent{Provider: p.opts.Name, Model: model}) {
		return
	}

	blocks := map[int]*blockAcc{}
	var usage llm.UsageEvent
	stopReason := llm.StopEnd

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ev, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			send(llm.ErrorEvent{Err: &llm.Error{Kind: llm.ErrTransient, Message: err.Error()}})
			return
		}
		if strings.TrimSpace(ev.Data) == "" {
			continue
		}

		switch ev.Name {
		case "message_start":
			var ms messageStart
			if json.Unmarshal([]byte(ev.Data), &ms) == nil {
				usage.Input = ms.Message.Usage.InputTokens
				usage.Cached = ms.Message.Usage.CacheReadInputTokens
			}
		case "content_block_start":
			var cb contentBlockStart
			if json.Unmarshal([]byte(ev.Data), &cb) != nil {
				continue
			}
			acc := &blockAcc{kind: cb.ContentBlock.Type}
			acc.toolID = cb.ContentBlock.ID
			acc.toolName = cb.ContentBlock.Name
			blocks[cb.Index] = acc
			if !send(llm.BlockStartEvent{Index: cb.Index, Type: mapBlockType(cb.ContentBlock.Type)}) {
				return
			}
		case "content_block_delta":
			var d contentBlockDelta
			if json.Unmarshal([]byte(ev.Data), &d) != nil {
				continue
			}
			acc := blocks[d.Index]
			switch d.Delta.Type {
			case "text_delta":
				if acc != nil {
					acc.text.WriteString(d.Delta.Text)
				}
				if !send(llm.TextDeltaEvent{Index: d.Index, Text: d.Delta.Text}) {
					return
				}
			case "thinking_delta":
				if acc != nil {
					acc.text.WriteString(d.Delta.Thinking)
				}
				if !send(llm.ThinkingDeltaEvent{Index: d.Index, Text: d.Delta.Thinking}) {
					return
				}
			case "input_json_delta":
				if acc != nil {
					acc.toolArgs.WriteString(d.Delta.PartialJSON)
				}
				if !send(llm.ToolArgsDeltaEvent{Index: d.Index, PartialJSON: d.Delta.PartialJSON}) {
					return
				}
			case "signature_delta":
				if acc != nil {
					acc.signature = d.Delta.Signature
				}
			}
		case "content_block_stop":
			var cb contentBlockStop
			if json.Unmarshal([]byte(ev.Data), &cb) != nil {
				continue
			}
			acc := blocks[cb.Index]
			if !send(llm.BlockStopEvent{Index: cb.Index}) {
				return
			}
			if acc != nil && acc.kind == "tool_use" {
				input, parseErr := llm.ParseToolInput(acc.toolArgs.String())
				if !send(llm.ToolCallReadyEvent{
					Index: cb.Index, ID: acc.toolID, Name: acc.toolName,
					Input: input, ParseErr: parseErr,
				}) {
					return
				}
			}
		case "message_delta":
			var md messageDelta
			if json.Unmarshal([]byte(ev.Data), &md) == nil {
				if md.Usage.InputTokens > 0 {
					usage.Input = md.Usage.InputTokens
				}
				usage.Output = md.Usage.OutputTokens
				usage.Cached = md.Usage.CacheReadInputTokens
				if md.Delta.StopReason != "" {
					stopReason = mapStopReason(md.Delta.StopReason)
				}
			}
		case "message_stop":
			// 终止事件
		case "ping":
			if !send(llm.PingEvent{}) {
				return
			}
		case "error":
			var e struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) == nil {
				send(llm.ErrorEvent{Err: &llm.Error{
					Kind:       mapErrorType(e.Error.Type),
					VendorCode: e.Error.Type,
					Message:    e.Error.Message,
				}})
				return
			}
		}
	}

	if usage.Input > 0 || usage.Output > 0 {
		if !send(usage) {
			return
		}
	}
	send(llm.StopEvent{Reason: stopReason})
}

func mapBlockType(t string) llm.BlockType {
	switch t {
	case "text":
		return llm.BlockText
	case "thinking", "redacted_thinking":
		return llm.BlockThinking
	case "tool_use":
		return llm.BlockToolUse
	case "image":
		return llm.BlockImage
	default:
		return llm.BlockText
	}
}

func mapStopReason(s string) llm.StopReason {
	switch s {
	case "end_turn", "stop_sequence":
		return llm.StopEnd
	case "tool_use":
		return llm.StopToolUse
	case "max_tokens":
		return llm.StopLength
	case "refusal":
		return llm.StopFiltered
	default:
		return llm.StopEnd
	}
}

func mapErrorType(t string) llm.ErrorKind {
	switch t {
	case "rate_limit_error":
		return llm.ErrRateLimited
	case "authentication_error", "permission_error":
		return llm.ErrAuth
	case "invalid_request_error":
		return llm.ErrInvalidRequest
	case "overloaded_error", "api_error":
		return llm.ErrTransient
	case "not_found_error":
		return llm.ErrInvalidRequest
	default:
		return llm.ErrUnknown
	}
}

// ---- 协议结构体 ----

type messageStart struct {
	Message struct {
		Usage struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlockStart struct {
	Index        int `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
}

type contentBlockDelta struct {
	Index int `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type contentBlockStop struct {
	Index int `json:"index"`
}

type messageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// parseHTTPError 走通用归一化，再补上 Anthropic 的 error.type 作为厂商码。
func parseHTTPError(resp *http.Response) error {
	e := llm.ParseHTTPError(resp)

	var wrapper struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(e.Raw, &wrapper) == nil && wrapper.Error.Type != "" {
		e.VendorCode = wrapper.Error.Type
	}
	return e
}

// toAnthropicTools 把统一工具定义翻译为 Anthropic 格式。
func toAnthropicTools(specs []llm.ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		var schema any = map[string]any{}
		if len(s.InputSchema) > 0 {
			_ = json.Unmarshal(s.InputSchema, &schema)
		}
		out = append(out, map[string]any{
			"name":         s.Name,
			"description":  s.Description,
			"input_schema": schema,
		})
	}
	return out
}

// toAnthropicToolChoice 把统一策略名映射为 Anthropic 的取值。
func toAnthropicToolChoice(choice string) map[string]any {
	switch choice {
	case llm.ToolChoiceNone:
		return map[string]any{"type": "none"}
	case llm.ToolChoiceRequired:
		return map[string]any{"type": "any"}
	default:
		return map[string]any{"type": "auto"}
	}
}
