package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// OpenAIOptions 配置 OpenAI 兼容后端。
type OpenAIOptions struct {
	Name       string
	BaseURL    string // 例如 https://api.siliconflow.cn/v1
	APIKeyENV  string // 环境变量名，不存明文
	APIKey     string
	Model      string
	Dims       int    // 模型维度。OpenAI 兼容协议不返回维度，只能由配置声明
	RerankPath string // 默认 /rerank
	Client     *http.Client
	Timeout    time.Duration
}

// OpenAICompat 实现 OpenAI 兼容的嵌入与重排后端。
//
// 覆盖硅基流动、阿里百炼、OpenAI、以及绝大多数自建服务。
type OpenAICompat struct {
	opts OpenAIOptions
}

// NewOpenAI 构造后端。
func NewOpenAI(opts OpenAIOptions) *OpenAICompat {
	if opts.Client == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		opts.Client = &http.Client{Timeout: timeout}
	}
	if opts.RerankPath == "" {
		opts.RerankPath = "/rerank"
	}
	return &OpenAICompat{opts: opts}
}

// Name 实现 Embedder。
func (o *OpenAICompat) Name() string {
	if o.opts.Name != "" {
		return o.opts.Name
	}
	return "openai-embed"
}

// Dimensions 返回配置声明的维度。OpenAI 兼容协议不返回维度信息，
// 未配置时返回 0，由调用方从首次响应推断。
func (o *OpenAICompat) Dimensions(string) int { return o.opts.Dims }

// Embed 实现 Embedder。
func (o *OpenAICompat) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	if len(texts) == 0 {
		return nil, Usage{}, nil
	}
	if model == "" {
		model = o.opts.Model
	}
	if model == "" {
		return nil, Usage{}, fmt.Errorf("%w: 未指定嵌入模型", ErrNotConfigured)
	}

	body := map[string]any{"model": model, "input": texts}
	raw, err := o.post(ctx, "/embeddings", body)
	if err != nil {
		return nil, Usage{}, err
	}

	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		// OpenAI 用 prompt_tokens；千问等厂商用 total_tokens。
		// 两个都读，否则接千问时成本统计恒为 0——不会报错，只会悄悄少记账。
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, Usage{}, fmt.Errorf("解析嵌入响应: %w", err)
	}
	if len(resp.Data) != len(texts) {
		return nil, Usage{}, fmt.Errorf("嵌入结果数量不符：期望 %d，实际 %d", len(texts), len(resp.Data))
	}

	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			continue
		}
		Normalize(d.Embedding)
		out[d.Index] = d.Embedding
	}

	tokens := resp.Usage.PromptTokens
	if tokens == 0 {
		tokens = resp.Usage.TotalTokens
	}
	return out, Usage{
		InputTokens: tokens,
		Requests:    1,
		Model:       model,
	}, nil
}

// Rerank 实现 Reranker。
//
// 走 /rerank 端点（硅基流动、Cohere、Jina 的兼容格式）。
func (o *OpenAICompat) Rerank(ctx context.Context, model, query string, docs []string) ([]float32, Usage, error) {
	if len(docs) == 0 {
		return nil, Usage{}, nil
	}
	if model == "" {
		model = "bge-reranker-v2-m3"
	}

	body := map[string]any{
		"model":            model,
		"query":            query,
		"documents":        docs,
		"return_documents": false,
	}
	raw, err := o.post(ctx, o.opts.RerankPath, body)
	if err != nil {
		return nil, Usage{}, err
	}

	var resp struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float32 `json:"relevance_score"`
		} `json:"results"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, Usage{}, fmt.Errorf("解析重排响应: %w", err)
	}

	scores := make([]float32, len(docs))
	for _, r := range resp.Results {
		if r.Index >= 0 && r.Index < len(scores) {
			scores[r.Index] = r.RelevanceScore
		}
	}
	return scores, Usage{
		InputTokens: resp.Usage.TotalTokens,
		Requests:    1,
		Model:       model,
	}, nil
}

func (o *OpenAICompat) post(ctx context.Context, path string, body map[string]any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(o.opts.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := o.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := o.opts.Client.Do(req)
	if err != nil {
		// 连不上不是「暂时不可用」：地址配错或服务没起，重试无意义
		return nil, fmt.Errorf("%w: %w: %v", ErrUnavailable, ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// ParseHTTPError 会消费响应体，必须在读取 raw 之前调用
		e := llm.ParseHTTPError(resp)
		return nil, fmt.Errorf("%w: %s（HTTP %d）", ErrUnavailable, e.Message, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (o *OpenAICompat) apiKey() string {
	if o.opts.APIKey != "" {
		return o.opts.APIKey
	}
	if o.opts.APIKeyENV != "" {
		return os.Getenv(o.opts.APIKeyENV)
	}
	return ""
}

var _ Embedder = (*OpenAICompat)(nil)
var _ Reranker = (*OpenAICompat)(nil)
