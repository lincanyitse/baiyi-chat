// Package config 负责加载配置：TOML 文件 + 环境变量覆盖。
//
// 优先级：环境变量 > 配置文件 > 内置默认值。
// 密钥只通过环境变量引用（api_key_env），配置文件里不写明文。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	DataDir string `koanf:"data_dir"`
	// SkillsDir 是技能目录，留空则用 <data_dir>/skills。
	SkillsDir string `koanf:"skills_dir"`
	// MCP 是外部工具服务器列表。
	MCP       []MCPServer         `koanf:"mcp"`
	Log       LogConfig           `koanf:"log"`
	Providers map[string]Provider `koanf:"providers"`
	Route     map[string]Route    `koanf:"route"`
	Session   SessionConfig       `koanf:"session"`
	Embed     EmbedConfig         `koanf:"embed"`
	Rerank    RerankConfig        `koanf:"rerank"`
	RAG       RAGConfig           `koanf:"rag"`
	Web       WebConfig           `koanf:"web"`
}

// WebConfig 配置 Web 渠道。
type WebConfig struct {
	// Listen 是监听地址。默认只监听回环，改成 0.0.0.0 会暴露到局域网。
	Listen string `koanf:"listen"`
	// ReadOnlyTools 为真时只把只读工具发给模型，写类与危险工具根本不下发。
	//
	// M3.1 没有审批弹窗，默认 true；M3.2 审批就绪后默认 false。
	// 保留这个开关而不是删掉：它是「最小授权」的兜底，用户可能
	// 想要一个永远不能改任何东西的只读助手。
	ReadOnlyTools bool `koanf:"read_only_tools"`
}

// IsLoopback 报告监听地址是否只对本机可见。
//
// 用 net.SplitHostPort 而不是手工找冒号：后者会把 "[::1]:8787"
// 切成 "[::1"，既不匹配 "::1" 也不报错，只是让非回环警告误报。
func (w WebConfig) IsLoopback() bool {
	host := w.Listen
	if h, _, err := net.SplitHostPort(w.Listen); err == nil {
		host = h
	}
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// EmbedConfig 配置文本嵌入后端。
//
// Kind 决定用哪个实现：openai | ollama | custom | fake | none。
// 留空视为 none——没有嵌入也能用关键词检索，只是召回变差。
type EmbedConfig struct {
	Kind      string `koanf:"kind"`
	BaseURL   string `koanf:"base_url"`
	APIKeyEnv string `koanf:"api_key_env"`
	Model     string `koanf:"model"`
	// Dims 是向量维度。OpenAI 兼容协议不返回维度，必须显式声明。
	Dims      int     `koanf:"dims"`
	BatchSize int     `koanf:"batch_size"`
	RPS       float64 `koanf:"rps"`
	TimeoutS  int     `koanf:"timeout_s"`

	// custom 后端专用
	Method          string            `koanf:"method"`
	Headers         map[string]string `koanf:"headers"`
	RequestTemplate string            `koanf:"request_template"`
	ResponsePath    string            `koanf:"response_path"`

	// 重排端点的路径，openai 兼容后端用
	RerankPath string `koanf:"rerank_path"`
}

// RerankConfig 配置重排后端。Kind 与 Embed 独立：可以不配嵌入而配重排，
// 反之亦然。
type RerankConfig struct {
	Kind      string `koanf:"kind"`
	BaseURL   string `koanf:"base_url"`
	APIKeyEnv string `koanf:"api_key_env"`
	Model     string `koanf:"model"`
	// Path 是重排端点路径，留空用 /rerank。
	Path     string  `koanf:"path"`
	RPS      float64 `koanf:"rps"`
	TimeoutS int     `koanf:"timeout_s"`
}

// RAGConfig 配置检索行为。
type RAGConfig struct {
	// TopKVector / TopKKeyword 是两路召回各自的候选数。
	TopKVector  int `koanf:"top_k_vector"`
	TopKKeyword int `koanf:"top_k_keyword"`
	// TopKRerank 是重排后保留条数。
	TopKRerank int `koanf:"top_k_rerank"`
	// ChunkTokens 是切分目标 token 数，Overlap 是重叠比例（0-1）。
	ChunkTokens  int     `koanf:"chunk_tokens"`
	ChunkOverlap float64 `koanf:"chunk_overlap"`
	// EnableRerank 为 false 时只用 RRF 融合分。
	EnableRerank bool `koanf:"enable_rerank"`
}

type LogConfig struct {
	Level      string `koanf:"level"`       // debug|info|warn|error
	Format     string `koanf:"format"`      // text|json
	File       string `koanf:"file"`        // 空则不写文件
	MaxSizeMB  int    `koanf:"max_size_mb"` // 轮转阈值
	MaxBackups int    `koanf:"max_backups"`
}

type Provider struct {
	Kind      string            `koanf:"kind"` // openai-compat|responses|anthropic
	BaseURL   string            `koanf:"base_url"`
	APIKeyEnv string            `koanf:"api_key_env"` // 环境变量名，不存明文
	TimeoutS  int               `koanf:"timeout_s"`
	Models    map[string]Model  `koanf:"models"`
	Headers   map[string]string `koanf:"headers"`
}

type Model struct {
	ContextWindow int     `koanf:"context_window"`
	MaxOutput     int     `koanf:"max_output"`
	Tools         bool    `koanf:"tools"`
	Vision        bool    `koanf:"vision"`
	Thinking      bool    `koanf:"thinking"`
	PrefixCache   bool    `koanf:"prefix_cache"`
	StrictSchema  bool    `koanf:"strict_schema"`
	PriceIn       float64 `koanf:"price_in"`  // 元 / 百万 token
	PriceOut      float64 `koanf:"price_out"` // 元 / 百万 token
	PriceCached   float64 `koanf:"price_cached"`
}

type Route struct {
	Candidates []string `koanf:"candidates"`
}

type SessionConfig struct {
	MaxSteps      int `koanf:"max_steps"`
	TurnTimeoutS  int `koanf:"turn_timeout_s"`
	StreamTimeout int `koanf:"stream_timeout_s"`
	MaxToolCalls  int `koanf:"max_tool_calls"`
}

// Default 返回内置默认值。所有字段都必须有可用的默认，保证零配置可启动。
func Default() Config {
	return Config{
		DataDir: defaultDataDir(),
		Log: LogConfig{
			Level:      "info",
			Format:     "text",
			MaxSizeMB:  20,
			MaxBackups: 5,
		},
		Providers: map[string]Provider{},
		// 路由留空：零配置启动没有可用模型，Web 会进入配置向导，
		// 而不是指向一台别人机器上不存在的网关。
		Route: map[string]Route{},
		Session: SessionConfig{
			MaxSteps:      12,
			TurnTimeoutS:  180,
			StreamTimeout: 120,
			MaxToolCalls:  20,
		},
		Embed: EmbedConfig{
			Kind:      "none",
			Model:     "bge-m3",
			BatchSize: 64,
			TimeoutS:  120,
		},
		Rerank: RerankConfig{
			Kind:     "none",
			Model:    "bge-reranker-v2-m3",
			TimeoutS: 60,
		},
		RAG: RAGConfig{
			TopKVector:   30,
			TopKKeyword:  30,
			TopKRerank:   10,
			ChunkTokens:  600,
			ChunkOverlap: 0.12,
			EnableRerank: true,
		},
		Web: WebConfig{
			Listen: "127.0.0.1:8787",
			// M3.2 起 Web 有了审批弹窗，写类工具可以放开；
			// 仍可用配置显式收窄成只读。
			ReadOnlyTools: false,
		},
	}
}

func defaultDataDir() string {
	if v := os.Getenv("BAIYI_DATA_DIR"); v != "" {
		return v
	}
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, ".baiyi")
		}
		return ".baiyi"
	}
	return filepath.Join(appdata, "baiyi")
}

// Load 从 configPath 读取 TOML（可为空，表示只用默认与环境变量），
// 环境变量 BAIYI_ 前缀覆盖，例如 BAIYI_LOG_LEVEL=debug。
func Load(configPath string) (Config, error) {
	k := koanf.New(".")

	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return Config{}, fmt.Errorf("load defaults: %w", err)
	}

	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
				return Config{}, fmt.Errorf("load %s: %w", configPath, err)
			}
		} else if !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("stat %s: %w", configPath, err)
		}
	}

	// BAIYI_ 前缀，双下划线映射层级：BAIYI_LOG__LEVEL=debug
	if err := k.Load(env.Provider("BAIYI_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "BAIYI_")
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "__", ".")
		return s
	}), nil); err != nil {
		return Config{}, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal: %w", err)
	}

	// BAIYI_DATA_DIR 既支持环境变量也支持配置项，环境变量优先
	if v := os.Getenv("BAIYI_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验配置。只读，不修改接收者。
//
// 早先这里有一句 `c.Log.Level = "info"`——值接收者上的赋值对调用方
// 毫无作用，是个静默失效的「默认值补全」。默认值统一由 Default() 负责，
// 校验函数不该兼职做归一化。
func (c Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("data_dir 不能为空")
	}
	for name, p := range c.Providers {
		if p.Kind != "openai-compat" && p.Kind != "responses" && p.Kind != "anthropic" {
			return fmt.Errorf("provider %q: 未知 kind %q（支持 openai-compat / responses / anthropic）", name, p.Kind)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url 不能为空", name)
		}
		// 不强制 api_key_env：密钥可能来自加密存储（界面填写），
		// 也可能先建 Provider 再补密钥。硬性要求会让界面路径走不通。
	}

	if err := c.Embed.Validate(); err != nil {
		return err
	}
	if err := c.Rerank.Validate(); err != nil {
		return err
	}
	if c.RAG.ChunkTokens <= 0 {
		return fmt.Errorf("rag.chunk_tokens 必须为正数")
	}
	if c.RAG.ChunkOverlap < 0 || c.RAG.ChunkOverlap >= 1 {
		return fmt.Errorf("rag.chunk_overlap 必须在 [0,1) 内")
	}
	for _, f := range []struct {
		name string
		v    int
	}{{"rag.top_k_vector", c.RAG.TopKVector}, {"rag.top_k_keyword", c.RAG.TopKKeyword}, {"rag.top_k_rerank", c.RAG.TopKRerank}} {
		if f.v <= 0 {
			return fmt.Errorf("%s 必须为正数", f.name)
		}
	}
	return nil
}

// Validate 检查嵌入配置。
//
// fake 与 none 不需要网络参数；其余后端必须给全，否则启动后
// 第一次检索才发现配错，那时用户已经在等结果。
func (e EmbedConfig) Validate() error {
	switch e.Kind {
	case "", "none", "fake":
		return nil
	case "openai", "ollama", "custom":
	default:
		return fmt.Errorf("embed.kind %q 未知（支持 openai / ollama / custom / fake / none）", e.Kind)
	}
	if e.BaseURL == "" {
		return fmt.Errorf("embed.base_url 不能为空（kind=%s）", e.Kind)
	}
	if e.Model == "" {
		return fmt.Errorf("embed.model 不能为空（kind=%s）", e.Kind)
	}
	if e.Kind == "custom" && e.ResponsePath == "" && e.RequestTemplate == "" {
		return fmt.Errorf("embed.kind=custom 至少需要 response_path 或 request_template")
	}
	if e.Dims < 0 {
		return fmt.Errorf("embed.dims 不能为负")
	}
	return nil
}

// Validate 检查重排配置。
func (r RerankConfig) Validate() error {
	switch r.Kind {
	case "", "none", "fake":
		return nil
	case "openai":
	default:
		return fmt.Errorf("rerank.kind %q 未知（支持 openai / fake / none）", r.Kind)
	}
	if r.BaseURL == "" {
		return fmt.Errorf("rerank.base_url 不能为空（kind=%s）", r.Kind)
	}
	if r.Model == "" {
		return fmt.Errorf("rerank.model 不能为空（kind=%s）", r.Kind)
	}
	return nil
}

// Enabled 报告是否配置了嵌入后端。
func (e EmbedConfig) Enabled() bool {
	return e.Kind != "" && e.Kind != "none"
}

// ResolveAPIKey 从环境变量读取密钥。缺失时返回空串，由调用方决定是否报错。
func (p Provider) ResolveAPIKey() string {
	if p.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(p.APIKeyEnv)
}

func (p Provider) Timeout() time.Duration {
	if p.TimeoutS <= 0 {
		return 120 * time.Second
	}
	return time.Duration(p.TimeoutS) * time.Second
}

// MCPServer 描述一个 MCP server 的配置。
type MCPServer struct {
	// Alias 是短名，用于工具命名空间，必须唯一。
	Alias string `koanf:"alias"`
	// Transport 是 "stdio" 或 "http"。
	Transport string `koanf:"transport"`
	// Command / Args 用于 stdio 传输。
	Command string   `koanf:"command"`
	Args    []string `koanf:"args"`
	// URL 用于 http 传输。
	URL string `koanf:"url"`
	// AllowedTools 为空表示全部允许。
	AllowedTools []string `koanf:"allowed_tools"`
	// Risk 是这个 server 全部工具的风险级别：read / write / dangerous。
	// 留空按 read 处理；未知值按 dangerous 处理（宁可多要一次审批）。
	Risk string `koanf:"risk"`
	// Enabled 为 false 时跳过该 server。
	Enabled bool `koanf:"enabled"`
}
