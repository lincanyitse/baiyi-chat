// Package openaicompat 实现 OpenAI Chat Completions 协议的流式适配器。
//
// 覆盖 DeepSeek / Qwen / GLM / Kimi / vLLM / Ollama 等绝大多数服务。
// 关键容错：tool_calls 参数是字符串分片；id/name 可能首片缺失或每片重发；
// usage 可能在最后一个空 choices 分片才到达。
package openaicompat

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
	"github.com/lincanyitse/baiyi-chat/internal/provider"
	"github.com/lincanyitse/baiyi-chat/internal/provider/sse"
)

// Options 是适配器构造参数。
type Options struct {
	Name    string // Provider 名称，用于日志与账本
	BaseURL string // 例如 http://127.0.0.1:8899/v1
	APIKey  string
	Headers map[string]string
	Client  *http.Client
	// Capabilities 按模型覆盖默认能力（从配置读入）。
	Capabilities func(model string) llm.Capabilities
	// ExtraBody 会被合并进请求体，用于厂商特有参数（如 Ollama 的 num_ctx）。
	ExtraBody map[string]any
}

// Provider 是 OpenAI 兼容协议适配器。
type Provider struct {
	opts Options
}

// New 构造适配器。
func New(opts Options) *Provider {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 0} // 超时由 ctx 控制
	}
	if opts.Capabilities == nil {
		opts.Capabilities = func(string) llm.Capabilities { return llm.DefaultOpenAICompat() }
	}
	return &Provider{opts: opts}
}

func (p *Provider) Name() string { return p.opts.Name }

func (p *Provider) Capabilities(model string) llm.Capabilities { return p.opts.Capabilities(model) }

// CountTokens 用启发式估算。中文约 0.7 token/字，英文约 1 token/3.5 字符。
func (p *Provider) CountTokens(_ context.Context, req llm.Request) (int, error) {
	return llm.EstimateTokens(req), nil
}

// ListModels 查询服务端的模型列表（GET /v1/models）。
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return provider.FetchModels(ctx, p.opts.Client, p.opts.BaseURL, p.opts.APIKey, p.opts.Headers)
}

// Stream 发起流式请求并返回归一化事件通道。
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
	if p.opts.APIKey != "" {
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
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func (p *Provider) buildBody(req llm.Request) (io.Reader, error) {
	payload := map[string]any{
		"model":    req.Model,
		"messages": p.convertMessages(req),
		"stream":   true,
		// 显式请求 usage，否则 OpenAI 生态多数服务不返回（极易漏）。
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		payload["stop"] = req.Stop
	}
	if len(req.Tools) > 0 {
		payload["tools"] = toOpenAITools(req.Tools)
		if req.ToolChoice != "" {
			payload["tool_choice"] = mapToolChoice(req.ToolChoice)
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

// convertMessages 把统一消息模型翻译成 OpenAI 格式。
func (p *Provider) convertMessages(req llm.Request) []map[string]any {
	var out []map[string]any

	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}

	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			out = append(out, map[string]any{"role": "system", "content": llm.TextOf(m)})
			continue
		}

		var texts []string
		var images []map[string]any
		var toolCalls []map[string]any
		var toolResults []map[string]any
		var reasoning strings.Builder

		for _, blk := range m.Content {
			switch b := blk.(type) {
			case llm.TextBlock:
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case llm.ImageBlock:
				images = append(images, toImagePart(b))
			case llm.FileBlock:
				images = append(images, toFilePart(b))
			case llm.ThinkingBlock:
				// OpenAI 系 reasoning_content 多数不可回传，丢弃。
				_ = reasoning
			case llm.ToolUseBlock:
				toolCalls = append(toolCalls, map[string]any{
					"id":   b.ID,
					"type": "function",
					"function": map[string]any{
						"name":      b.Name,
						"arguments": string(b.Input),
					},
				})
			case llm.ToolResultBlock:
				content := llm.TextOfBlocks(b.Content)
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": b.ToolUseID,
					"content":      content,
				})
			}
		}

		if len(images) > 0 {
			var parts []map[string]any
			for _, t := range texts {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
			parts = append(parts, images...)
			out = append(out, map[string]any{"role": string(m.Role), "content": parts})
			continue
		}

		if len(toolCalls) > 0 {
			msg := map[string]any{"role": "assistant", "tool_calls": toolCalls}
			if len(texts) > 0 {
				msg["content"] = strings.Join(texts, "")
			}
			out = append(out, msg)
		} else if len(texts) > 0 || len(toolResults) == 0 {
			out = append(out, map[string]any{"role": string(m.Role), "content": strings.Join(texts, "")})
		}

		out = append(out, toolResults...)
	}
	return out
}

func toImagePart(b llm.ImageBlock) map[string]any {
	var url string
	switch {
	case b.Source.URL != "":
		url = b.Source.URL
	default:
		mt := b.Source.MediaType
		if mt == "" {
			mt = "image/png"
		}
		url = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b.Source.Data)
	}
	img := map[string]any{"url": url}
	if b.Detail != "" {
		img["detail"] = b.Detail
	}
	return map[string]any{"type": "image_url", "image_url": img}
}

func toFilePart(b llm.FileBlock) map[string]any {
	mt := b.MediaType
	if mt == "" {
		mt = "application/octet-stream"
	}
	file := map[string]any{
		"filename":  b.Name,
		"file_data": "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b.Data),
	}
	return map[string]any{"type": "file", "file": file}
}

// pump 解析 SSE 流并发出归一化事件。
type toolCallAcc struct {
	id   string
	name string
	args strings.Builder
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

	var (
		textIndex  = -1
		thinkIndex = -1
		tools      = map[int]*toolCallAcc{}
		blockIndex int
		stopReason = llm.StopEnd
		usage      *llm.UsageEvent
	)

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

		data := strings.TrimSpace(ev.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// 个别厂商会插入非 JSON 的心跳行，跳过而非中断。
			continue
		}
		if chunk.Error != nil {
			send(llm.ErrorEvent{Err: mapVendorError(chunk.Error)})
			return
		}

		if u := chunk.Usage; u != nil {
			usage = &llm.UsageEvent{
				Input:     u.PromptTokens,
				Output:    u.CompletionTokens,
				Cached:    u.PromptTokensDetails.CachedTokens,
				Reasoning: u.CompletionTokensDetails.ReasoningTokens,
			}
		}

		for _, choice := range chunk.Choices {
			// 单块内可能同时有 reasoning 与 content，按出现顺序发。
			if txt := choice.Delta.Content; txt != nil && *txt != "" {
				if textIndex < 0 {
					textIndex = blockIndex
					blockIndex++
					if !send(llm.BlockStartEvent{Index: textIndex, Type: llm.BlockText}) {
						return
					}
				}
				if !send(llm.TextDeltaEvent{Index: textIndex, Text: *txt}) {
					return
				}
			}

			if txt := choice.Delta.ReasoningContent; txt != nil && *txt != "" {
				if thinkIndex < 0 {
					thinkIndex = blockIndex
					blockIndex++
					if !send(llm.BlockStartEvent{Index: thinkIndex, Type: llm.BlockThinking}) {
						return
					}
				}
				if !send(llm.ThinkingDeltaEvent{Index: thinkIndex, Text: *txt}) {
					return
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				acc, ok := tools[tc.Index]
				if !ok {
					acc = &toolCallAcc{}
					tools[tc.Index] = acc
				}
				// id / name 可能首片缺失或每片重发，只在为空时赋值。
				if acc.id == "" && tc.ID != "" {
					acc.id = tc.ID
				}
				if acc.name == "" && tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.args.WriteString(tc.Function.Arguments)
				}
			}

			if fr := choice.FinishReason; fr != nil {
				stopReason = mapFinishReason(*fr)
			}
		}
	}

	// 结束块
	if thinkIndex >= 0 && !send(llm.BlockStopEvent{Index: thinkIndex}) {
		return
	}
	if textIndex >= 0 && !send(llm.BlockStopEvent{Index: textIndex}) {
		return
	}

	// 发出完整校验过的工具调用
	for idx := 0; idx < len(tools); idx++ {
		acc, ok := tools[idx]
		if !ok {
			continue
		}
		input, parseErr := llm.ParseToolInput(acc.args.String())
		if !send(llm.ToolCallReadyEvent{
			Index: idx, ID: acc.id, Name: acc.name,
			Input: input, ParseErr: parseErr,
		}) {
			return
		}
	}

	if usage != nil {
		if !send(*usage) {
			return
		}
	}
	send(llm.StopEvent{Reason: stopReason})
}

func mapFinishReason(s string) llm.StopReason {
	switch s {
	case "stop":
		return llm.StopEnd
	case "tool_calls", "function_call":
		return llm.StopToolUse
	case "length":
		return llm.StopLength
	case "content_filter":
		return llm.StopFiltered
	default:
		return llm.StopEnd
	}
}

// ---- 协议结构体 ----

type chatChunk struct {
	ID      string        `json:"id"`
	Choices []chunkChoice `json:"choices"`
	Usage   *chunkUsage   `json:"usage"`
	Error   *vendorError  `json:"error"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role             string          `json:"role"`
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	ToolCalls        []chunkToolCall `json:"tool_calls"`
}

type chunkToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chunkUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type vendorError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func mapVendorError(e *vendorError) *llm.Error {
	msg := e.Message
	code := fmt.Sprint(e.Code)
	if e.Code == nil {
		code = ""
	}
	kind := llm.ErrUnknown
	switch {
	case strings.Contains(msg, "rate") || strings.Contains(msg, "429"):
		kind = llm.ErrRateLimited
	case strings.Contains(msg, "context") || strings.Contains(msg, "too long"):
		kind = llm.ErrContextOverflow
	}
	return &llm.Error{Kind: kind, VendorCode: code, Message: msg}
}

// parseHTTPError 先走通用的错误归一化，再处理 ModSwitch 网关特有的嵌套格式：
// 它把厂商原始错误塞进 error.message 的 JSON 字符串里。
func parseHTTPError(resp *http.Response) error {
	e := llm.ParseHTTPError(resp)

	msg := e.Message
	var inner struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal([]byte(msg), &inner) == nil && inner.Msg != "" {
		e.Message = inner.Msg
		if inner.Code != 0 {
			e.VendorCode = fmt.Sprintf("%d", inner.Code)
		}
	}
	return e
}

// toOpenAITools 把统一工具定义翻译为 OpenAI 的 function 包装格式。
func toOpenAITools(specs []llm.ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		var params any = map[string]any{}
		if len(s.InputSchema) > 0 {
			_ = json.Unmarshal(s.InputSchema, &params)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.Name,
				"description": s.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// mapToolChoice 把统一策略名映射为 OpenAI 的取值。
func mapToolChoice(choice string) string {
	switch choice {
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	default:
		return "auto"
	}
}
