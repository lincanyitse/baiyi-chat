package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// CustomOptions 配置自定义 HTTP 后端。
//
// 面向「不属于任何主流协议，但用户自己搭了一个」的场景：
// 请求体是一段带占位符的 JSON 模板，响应用点路径取向量。
type CustomOptions struct {
	Name    string
	URL     string
	Method  string // 默认 POST
	Headers map[string]string
	// RequestTemplate 是 JSON 文本，支持两个占位符：
	//   {{model}} 替换为模型名（已做 JSON 转义）
	//   {{text}}  替换为当前文本（已做 JSON 转义）
	// 默认 `{"model":"{{model}}","input":"{{text}}"}`。
	RequestTemplate string
	// ResponsePath 是响应里向量的点路径，支持数组下标：
	//   "embedding"、"data.0.embedding"、"result.vectors.0"
	// 若响应本身就是向量数组，写 "-"。
	ResponsePath string
	Model        string
	Dims         int
	APIKeyENV    string
	APIKey       string
	Client       *http.Client
	Timeout      time.Duration
}

// Custom 实现完全自定义的嵌入后端。
type Custom struct {
	opts     CustomOptions
	template any // 预解析的模板，避免每次请求重新解析
}

// NewCustom 构造后端。
func NewCustom(opts CustomOptions) *Custom {
	if opts.Client == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		opts.Client = &http.Client{Timeout: timeout}
	}
	if opts.Method == "" {
		opts.Method = http.MethodPost
	}
	if opts.ResponsePath == "" {
		opts.ResponsePath = "embedding"
	}
	tmpl := opts.RequestTemplate
	if tmpl == "" {
		tmpl = `{"model":"{{model}}","input":"{{text}}"}`
	}
	// 模板非法时保留原始文本，到 Embed 时才报错——构造期不能失败，
	// 否则配置错误会让整个程序起不来。
	var parsed any = tmpl
	var doc any
	if json.Unmarshal([]byte(tmpl), &doc) == nil {
		parsed = doc
	}
	return &Custom{opts: opts, template: parsed}
}

// Name 实现 Embedder。
func (c *Custom) Name() string {
	if c.opts.Name != "" {
		return c.opts.Name
	}
	return "custom"
}

// Dimensions 实现 Embedder。
func (c *Custom) Dimensions(string) int { return c.opts.Dims }

// Embed 实现 Embedder。
func (c *Custom) Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error) {
	if len(texts) == 0 {
		return nil, Usage{}, nil
	}
	if model == "" {
		model = c.opts.Model
	}
	if c.opts.URL == "" {
		return nil, Usage{}, fmt.Errorf("%w: 未配置 URL", ErrNotConfigured)
	}

	out := make([][]float32, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, Usage{}, err
		}
		vec, err := c.embedOne(ctx, model, text)
		if err != nil {
			return nil, Usage{}, err
		}
		out[i] = vec
	}
	return out, Usage{Requests: len(texts), Model: model}, nil
}

func (c *Custom) embedOne(ctx context.Context, model, text string) ([]float32, error) {
	body, err := renderTemplate(c.template, model, text)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, c.opts.Method, c.opts.URL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	if key := c.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.opts.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w: %v", ErrUnavailable, ErrUnreachable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d：%s", ErrUnavailable, resp.StatusCode, llm.Truncate(string(raw), 200))
	}

	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析自定义嵌入响应: %w", err)
	}
	vec, err := extractVector(doc, c.opts.ResponsePath)
	if err != nil {
		return nil, err
	}
	Normalize(vec)
	return vec, nil
}

// renderTemplate 把模板里的占位符替换成实际值并序列化为 JSON。
//
// 替换发生在**解析后的 JSON 值**上，转义交给 encoding/json：
// 若在原始文本上做字符串替换，含引号、反斜杠、换行的文本会产出非法 JSON，
// 且很难在用户侧察觉。
func renderTemplate(tmpl any, model, text string) (string, error) {
	substituted := substitute(tmpl, model, text)
	body, err := json.Marshal(substituted)
	if err != nil {
		return "", fmt.Errorf("自定义请求模板序列化失败: %w", err)
	}
	return string(body), nil
}

func substitute(node any, model, text string) any {
	switch v := node.(type) {
	case string:
		return strings.NewReplacer("{{model}}", model, "{{text}}", text).Replace(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[k] = substitute(child, model, text)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = substitute(child, model, text)
		}
		return out
	default:
		return v
	}
}

func (c *Custom) apiKey() string {
	if c.opts.APIKey != "" {
		return c.opts.APIKey
	}
	if c.opts.APIKeyENV != "" {
		return os.Getenv(c.opts.APIKeyENV)
	}
	return ""
}

// extractVector 按点路径从 JSON 文档中取浮点数组。
func extractVector(doc any, path string) ([]float32, error) {
	if path == "-" {
		return toFloats(doc)
	}
	cur := doc
	for seg := range strings.SplitSeq(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, fmt.Errorf("响应中缺少字段 %q（路径 %s）", seg, path)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("响应路径下标非法 %q（路径 %s）", seg, path)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("响应路径 %s 在 %q 处无法继续", path, seg)
		}
	}
	return toFloats(cur)
}

func toFloats(v any) ([]float32, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("期望向量数组，实际是 %T", v)
	}
	out := make([]float32, len(arr))
	for i, x := range arr {
		f, ok := x.(float64)
		if !ok {
			return nil, fmt.Errorf("向量第 %d 项不是数字（%T）", i, x)
		}
		out[i] = float32(f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("响应中的向量为空")
	}
	return out, nil
}

var _ Embedder = (*Custom)(nil)
