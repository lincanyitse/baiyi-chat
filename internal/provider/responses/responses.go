// Package responses 实现 OpenAI Responses API（/v1/responses）的流式适配器。
//
// 与 Chat Completions 的差异：
//   - 输入用 input（字符串或消息数组），输出是 output item 数组
//   - 流式是具名事件（response.created / .output_item.added / .output_text.delta / .completed）
//   - 工具调用是 function_call item，参数走 function_call_arguments.delta
//   - usage 在 response.completed 事件里
package responses

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
	Name         string
	BaseURL      string // 例如 http://127.0.0.1:8899/v1
	APIKey       string
	Headers      map[string]string
	Client       *http.Client
	Capabilities func(model string) llm.Capabilities
	ExtraBody    map[string]any
}

// Provider 是 Responses API 适配器。
type Provider struct {
	opts Options
}

// New 构造适配器。
func New(opts Options) *Provider {
	if opts.Client == nil {
		opts.Client = &http.Client{}
	}
	if opts.Capabilities == nil {
		opts.Capabilities = func(string) llm.Capabilities { return llm.DefaultOpenAICompat() }
	}
	return &Provider{opts: opts}
}

func (p *Provider) Name() string { return p.opts.Name }

func (p *Provider) Capabilities(model string) llm.Capabilities { return p.opts.Capabilities(model) }

func (p *Provider) CountTokens(_ context.Context, req llm.Request) (int, error) {
	return llm.EstimateTokens(req), nil
}

// ListModels 查询服务端的模型列表（GET /v1/models）。
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return provider.FetchModels(ctx, p.opts.Client, p.opts.BaseURL, p.opts.APIKey, p.opts.Headers)
}

func (p *Provider) endpoint() string {
	base := strings.TrimSuffix(p.opts.BaseURL, "/")
	base = strings.TrimSuffix(base, "/responses")
	if strings.HasSuffix(base, "/v1") {
		return base + "/responses"
	}
	return base + "/v1/responses"
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

func (p *Provider) buildBody(req llm.Request) (io.Reader, error) {
	payload := map[string]any{
		"model":  req.Model,
		"input":  p.convertInput(req),
		"stream": true,
	}
	if req.System != "" {
		payload["instructions"] = req.System
	}
	if req.MaxTokens > 0 {
		payload["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.Tools) > 0 {
		payload["tools"] = toResponsesTools(req.Tools)
		if req.ToolChoice != "" {
			payload["tool_choice"] = mapResponsesToolChoice(req.ToolChoice)
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

// convertInput 把统一消息翻译成 Responses 的 input 数组。
func (p *Provider) convertInput(req llm.Request) []map[string]any {
	var out []map[string]any
	for _, m := range req.Messages {
		var parts []map[string]any
		for _, blk := range m.Content {
			switch b := blk.(type) {
			case llm.TextBlock:
				if b.Text == "" {
					continue
				}
				textType := "input_text"
				if m.Role == llm.RoleAssistant {
					textType = "output_text"
				}
				parts = append(parts, map[string]any{"type": textType, "text": b.Text})
			case llm.ImageBlock:
				// 有 URL 用 URL，否则转 base64 data URI。
				// 早先这里直接 continue 丢弃二进制图片——用户以为发出去了，
				// 实际模型什么也没收到，比报错更糟。
				src := b.Source.URL
				if src == "" {
					mt := b.Source.MediaType
					if mt == "" {
						mt = "image/png"
					}
					src = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b.Source.Data)
				}
				parts = append(parts, map[string]any{"type": "input_image", "image_url": src})
			case llm.FileBlock:
				mt := b.MediaType
				if mt == "" {
					mt = "application/octet-stream"
				}
				parts = append(parts, map[string]any{
					"type":     "input_file",
					"filename": b.Name,
					"file_data": "data:" + mt + ";base64," +
						base64.StdEncoding.EncodeToString(b.Data),
				})
			case llm.ToolUseBlock:
				// function_call 作为独立 item 回传
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   b.ID,
					"name":      b.Name,
					"arguments": string(b.Input),
				})
			case llm.ToolResultBlock:
				content := llm.TextOfBlocks(b.Content)
				out = append(out, map[string]any{
					"type":    "function_call_output",
					"call_id": b.ToolUseID,
					"output":  content,
				})
			}
		}
		if len(parts) > 0 {
			out = append(out, map[string]any{
				"type":    "message",
				"role":    string(m.Role),
				"content": parts,
			})
		}
	}
	return out
}

// pump 解析 Responses 具名事件流。
type itemAcc struct {
	kind       string // reasoning | message | function_call
	callID     string
	name       string
	args       strings.Builder
	textIndex  int
	thinkIndex int
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

	items := map[string]*itemAcc{}
	blockIndex := 0
	var usage llm.UsageEvent
	stopReason := llm.StopEnd
	hasToolUse := false

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

		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &head); err != nil {
			continue
		}

		switch head.Type {
		case "response.created":
			// 起始事件

		case "response.output_item.added":
			var e struct {
				Item struct {
					ID     string `json:"id"`
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				continue
			}
			acc := &itemAcc{kind: e.Item.Type, callID: e.Item.CallID, name: e.Item.Name}
			items[e.Item.ID] = acc

			switch e.Item.Type {
			case "reasoning":
				acc.thinkIndex = blockIndex
				blockIndex++
				if !send(llm.BlockStartEvent{Index: acc.thinkIndex, Type: llm.BlockThinking}) {
					return
				}
			case "message":
				acc.textIndex = blockIndex
				blockIndex++
				if !send(llm.BlockStartEvent{Index: acc.textIndex, Type: llm.BlockText}) {
					return
				}
			case "function_call":
				// 工具调用不占用文本块索引，结束时直接发 ToolCallReady
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var e struct {
				ItemID string `json:"item_id"`
				Delta  string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				continue
			}
			if acc := items[e.ItemID]; acc != nil {
				if !send(llm.ThinkingDeltaEvent{Index: acc.thinkIndex, Text: e.Delta}) {
					return
				}
			}

		case "response.output_text.delta":
			var e struct {
				ItemID string `json:"item_id"`
				Delta  string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				continue
			}
			if acc := items[e.ItemID]; acc != nil {
				if !send(llm.TextDeltaEvent{Index: acc.textIndex, Text: e.Delta}) {
					return
				}
			}

		case "response.function_call_arguments.delta":
			var e struct {
				ItemID string `json:"item_id"`
				Delta  string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				continue
			}
			if acc := items[e.ItemID]; acc != nil {
				acc.args.WriteString(e.Delta)
				if !send(llm.ToolArgsDeltaEvent{Index: -1, PartialJSON: e.Delta}) {
					return
				}
			}

		case "response.output_item.done":
			var e struct {
				Item struct {
					ID        string `json:"id"`
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				continue
			}
			acc := items[e.Item.ID]
			switch e.Item.Type {
			case "reasoning":
				if acc != nil && !send(llm.BlockStopEvent{Index: acc.thinkIndex}) {
					return
				}
			case "message":
				if acc != nil && !send(llm.BlockStopEvent{Index: acc.textIndex}) {
					return
				}
			case "function_call":
				hasToolUse = true
				callID := e.Item.CallID
				name := e.Item.Name
				args := e.Item.Arguments
				if acc != nil {
					if callID == "" {
						callID = acc.callID
					}
					if name == "" {
						name = acc.name
					}
					if args == "" {
						args = acc.args.String()
					}
				}
				input, parseErr := llm.ParseToolInput(args)
				if !send(llm.ToolCallReadyEvent{
					Index: 0, ID: callID, Name: name,
					Input: input, ParseErr: parseErr,
				}) {
					return
				}
			}

		case "response.completed":
			var e struct {
				Response struct {
					Usage struct {
						InputTokens        int `json:"input_tokens"`
						OutputTokens       int `json:"output_tokens"`
						InputTokensDetails struct {
							CachedTokens int `json:"cached_tokens"`
						} `json:"input_tokens_details"`
						OutputTokensDetails struct {
							ReasoningTokens int `json:"reasoning_tokens"`
						} `json:"output_tokens_details"`
					} `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(ev.Data), &e) == nil {
				usage = llm.UsageEvent{
					Input:     e.Response.Usage.InputTokens,
					Output:    e.Response.Usage.OutputTokens,
					Cached:    e.Response.Usage.InputTokensDetails.CachedTokens,
					Reasoning: e.Response.Usage.OutputTokensDetails.ReasoningTokens,
				}
			}

		case "response.failed", "error":
			var e struct {
				Response struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				} `json:"response"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			msg := "response failed"
			code := ""
			if json.Unmarshal([]byte(ev.Data), &e) == nil {
				if e.Response.Error.Message != "" {
					msg, code = e.Response.Error.Message, e.Response.Error.Code
				} else if e.Error.Message != "" {
					msg, code = e.Error.Message, e.Error.Code
				}
			}
			send(llm.ErrorEvent{Err: &llm.Error{
				Kind: llm.ErrUnknown, VendorCode: code, Message: msg,
			}})
			return
		}
	}

	if hasToolUse {
		stopReason = llm.StopToolUse
	}
	if usage.Input > 0 || usage.Output > 0 {
		if !send(usage) {
			return
		}
	}
	send(llm.StopEvent{Reason: stopReason})
}

// parseHTTPError 走通用错误归一化。Responses 的错误体与 Chat Completions 同构。
func parseHTTPError(resp *http.Response) error {
	return llm.ParseHTTPError(resp)
}

// toResponsesTools 把统一工具定义翻译为 Responses API 的扁平格式
// （与 Chat Completions 不同，这里没有 function 包装层）。
func toResponsesTools(specs []llm.ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		var params any = map[string]any{}
		if len(s.InputSchema) > 0 {
			_ = json.Unmarshal(s.InputSchema, &params)
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        s.Name,
			"description": s.Description,
			"parameters":  params,
		})
	}
	return out
}

// mapResponsesToolChoice 把统一策略名映射为 Responses 的取值。
func mapResponsesToolChoice(choice string) string {
	switch choice {
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	default:
		return "auto"
	}
}
