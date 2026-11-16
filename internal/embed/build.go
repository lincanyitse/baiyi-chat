package embed

import (
	"fmt"
	"net/http"
	"time"
)

// Settings 是构造嵌入后端所需的全部参数。
//
// 用独立的扁平结构而不是直接吃配置对象：embed 是中立能力包，
// 不该依赖 config（config 会拖进 koanf、文件系统、环境变量一整片）。
// bootstrap 负责把 config 翻译成 Settings。
type Settings struct {
	Kind      string
	BaseURL   string
	APIKey    string
	APIKeyENV string
	Model     string
	Dims      int
	BatchSize int
	RPS       float64
	Timeout   time.Duration

	Method          string
	Headers         map[string]string
	RequestTemplate string
	ResponsePath    string
	RerankPath      string
}

// RerankSettings 构造重排后端的参数。
type RerankSettings struct {
	Kind      string
	BaseURL   string
	APIKeyENV string
	Model     string
	Path      string
	RPS       float64
	Timeout   time.Duration
}

// Build 按 Settings 构造嵌入后端。返回 nil 表示未配置（调用方应降级）。
//
// 配置了后端时返回值是 *Limiter，限流、批切分、重试与记账对调用方透明。
func Build(s Settings) (Embedder, error) {
	if s.Kind == "" || s.Kind == "none" {
		return nil, nil
	}

	client := &http.Client{Timeout: s.Timeout}

	var inner Embedder
	switch s.Kind {
	case "fake":
		inner = NewFake(s.Dims)
	case "openai":
		inner = NewOpenAI(OpenAIOptions{
			BaseURL:    s.BaseURL,
			APIKey:     s.APIKey,
			APIKeyENV:  s.APIKeyENV,
			Model:      s.Model,
			Dims:       s.Dims,
			RerankPath: s.RerankPath,
			Client:     client,
		})
	case "ollama":
		inner = NewOllama(OllamaOptions{
			BaseURL: s.BaseURL,
			Model:   s.Model,
			Dims:    s.Dims,
			Client:  client,
		})
	case "custom":
		inner = NewCustom(CustomOptions{
			URL:             s.BaseURL,
			Method:          s.Method,
			Headers:         s.Headers,
			RequestTemplate: s.RequestTemplate,
			ResponsePath:    s.ResponsePath,
			Model:           s.Model,
			Dims:            s.Dims,
			APIKeyENV:       s.APIKeyENV,
			APIKey:          s.APIKey,
			Client:          client,
		})
	default:
		return nil, fmt.Errorf("未知的嵌入后端 %q", s.Kind)
	}

	// 部分后端（目前是 openai 兼容）同时提供重排端点。
	// 用能力接口探测，而不是断言具体类型——新增后端只要实现
	// Reranker 就自动获得该能力，Build 不需要认识它。
	var reranker Reranker
	if r, ok := inner.(Reranker); ok {
		reranker = r
	}

	return NewLimiter(inner, reranker, LimiterOptions{
		RPS:       s.RPS,
		BatchSize: s.BatchSize,
	}), nil
}

// BuildReranker 构造独立的重排后端。
//
// 与 Build 分开是因为两者常来自不同厂商：嵌入跑本地 Ollama，
// 重排走云端 API。塞进同一个配置会逼用户二选一。
func BuildReranker(s RerankSettings) (Reranker, error) {
	if s.Kind == "" || s.Kind == "none" {
		return nil, nil
	}
	if s.Kind == "fake" {
		return &FakeReranker{}, nil
	}
	if s.Kind != "openai" {
		return nil, fmt.Errorf("未知的重排后端 %q", s.Kind)
	}

	inner := NewOpenAI(OpenAIOptions{
		BaseURL:    s.BaseURL,
		APIKeyENV:  s.APIKeyENV,
		Model:      s.Model,
		RerankPath: s.Path,
		Client:     &http.Client{Timeout: s.Timeout},
	})
	return NewLimiter(inner, inner, LimiterOptions{RPS: s.RPS}), nil
}
