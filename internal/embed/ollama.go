package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/textutil"
)

// OllamaOptions 配置 Ollama 后端。
type OllamaOptions struct {
	Name    string
	BaseURL string // 例如 http://127.0.0.1:11434
	Model   string
	Dims    int // 模型维度；bge-m3 为 1024
	Client  *http.Client
	Timeout time.Duration
}

// Ollama 实现本地 Ollama 的嵌入协议。
//
// 与 OpenAI 兼容协议的区别：
//   - 单条请求（/api/embeddings 只收一个 prompt），批量在客户端并发
//   - 响应字段是 embedding 而非 data[].embedding
//   - 无重排能力
//
// 新版 /api/embed 支持批量，但它对老版本不兼容；批量由 limiter 层
// 并发发起单条请求实现，反而能更好地控制并发度。
type Ollama struct {
	opts OllamaOptions
}

// NewOllama 构造后端。
func NewOllama(opts OllamaOptions) *Ollama {
	if opts.Client == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 120 * time.Second // 本地 CPU 推理较慢
		}
		opts.Client = &http.Client{Timeout: timeout}
	}
	return &Ollama{opts: opts}
}

// Name 实现 Embedder。
func (o *Ollama) Name() string {
	if o.opts.Name != "" {
		return o.opts.Name
	}
	return "ollama"
}

// Dimensions 实现 Embedder。
func (o *Ollama) Dimensions(string) int { return o.opts.Dims }

// Embed 实现 Embedder。
//
// 逐条请求后并发收集：Ollama 的 /api/embeddings 是单条接口。
// 并发度由上层 limiter 控制，这里只保证顺序正确。
func (o *Ollama) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	if len(texts) == 0 {
		return nil, Usage{}, nil
	}
	if model == "" {
		model = o.opts.Model
	}
	if model == "" {
		return nil, Usage{}, fmt.Errorf("%w: 未指定嵌入模型", ErrNotConfigured)
	}

	// 串行执行：Ollama 本地服务通常单实例，并发请求只会互相拖慢，
	// 且 embeddding 模型在 CPU 上是计算密集型。
	out := make([][]float32, len(texts))
	tokens := 0
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, Usage{}, err
		}
		vec, used, err := o.embedOne(ctx, model, text)
		if err != nil {
			return nil, Usage{}, err
		}
		out[i] = vec
		tokens += used
	}

	return out, Usage{InputTokens: tokens, Requests: len(texts), Model: model}, nil
}

// maxOllamaRunes 是单次请求的字符上限。
//
// Ollama 的嵌入上下文默认 4096 token，超出直接返回 500。中文约 1.5 字/token，
// 这里按 3000 字留足余量。截断而不是报错：切分器本应保证 chunk 不这么大，
// 但代码块与表格是原子块，超长时会整块保留——那些必须能索引，哪怕截掉尾部。
const maxOllamaRunes = 3000

func (o *Ollama) embedOne(ctx context.Context, model, text string) ([]float32, int, error) {
	text = textutil.HeadRunes(text, maxOllamaRunes)

	body := map[string]any{"model": model, "prompt": text}
	raw, err := o.post(ctx, "/api/embeddings", body)
	if err != nil {
		return nil, 0, err
	}

	var resp struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 0, fmt.Errorf("解析 Ollama 嵌入响应: %w", err)
	}
	if len(resp.Embedding) == 0 {
		return nil, 0, fmt.Errorf("Ollama 返回空向量，模型 %s 可能未拉取", model)
	}
	Normalize(resp.Embedding)
	// Ollama 不返回 token 用量，用字符数粗估：中文约 1.5 字/token
	return resp.Embedding, len(text) * 2 / 3, nil
}

func (o *Ollama) post(ctx context.Context, path string, body map[string]any) ([]byte, error) {
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

	resp, err := o.opts.Client.Do(req)
	if err != nil {
		// 连不上不是「暂时不可用」：地址配错或服务没起，重试无意义
		return nil, fmt.Errorf("%w: %w: %v", ErrUnavailable, ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(string(raw))

		// Ollama 用 500 表示「输入超长」这类客户端错误。它重试多少次
		// 都一样，必须归为不可重试，否则一张大表格就能把整次索引拖慢几十秒。
		if strings.Contains(msg, "exceeds the context length") ||
			strings.Contains(msg, "input length exceeds") {
			return nil, fmt.Errorf("%w: Ollama 输入超长：%s", ErrUnsupportedInput, msg)
		}
		return nil, fmt.Errorf("%w: Ollama 返回 %d：%s", ErrUnavailable, resp.StatusCode, msg)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

var _ Embedder = (*Ollama)(nil)

// truncateRunes 按字符（不是字节）截断，避免切坏多字节字符。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
