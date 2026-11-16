package llm

// Capabilities 是某 Provider 某模型支持特性的声明。
//
// 能力是「协商」不是「事实」：自建 vLLM / Ollama 差异极大，
// 配置必须能覆盖声明。请求构建时若所需能力未声明，走降级而非等 400。
//
// 字段按引入的里程碑保留：
//   - M0 已使用：ContextWindow / MaxOutput / Tools / Vision / Thinking / PrefixCache
//   - M1 工具与上下文组装需要：ParallelTools / ToolNamePattern / MaxToolResultBytes /
//     StrictSchema / RequiresAlternate / MinCacheTokens
//   - 价格不在此声明：价格是带生效日期的独立数据，归 cost 包管，避免双轨。
type Capabilities struct {
	ContextWindow      int    `json:"context_window"`
	MaxOutput          int    `json:"max_output"`
	Tools              bool   `json:"tools"`
	ParallelTools      bool   `json:"parallel_tools"`
	Vision             bool   `json:"vision"`
	Audio              bool   `json:"audio"`
	Thinking           bool   `json:"thinking"`
	PrefixCache        bool   `json:"prefix_cache"`
	MinCacheTokens     int    `json:"min_cache_tokens"`
	StrictSchema       bool   `json:"strict_schema"`
	RequiresAlternate  bool   `json:"requires_alternate"` // 是否要求角色严格交替
	ToolNamePattern    string `json:"tool_name_pattern"`
	MaxToolResultBytes int    `json:"max_tool_result_bytes"`
}

// DefaultOpenAICompat 返回 OpenAI 兼容服务的保守默认能力。
func DefaultOpenAICompat() Capabilities {
	return Capabilities{
		ContextWindow:      128000,
		MaxOutput:          8192,
		Tools:              true,
		ParallelTools:      true,
		ToolNamePattern:    `^[a-zA-Z0-9_-]{1,64}$`,
		MaxToolResultBytes: 32 * 1024,
	}
}

// DefaultAnthropic 返回 Anthropic 原生协议默认能力。
func DefaultAnthropic() Capabilities {
	return Capabilities{
		ContextWindow:      200000,
		MaxOutput:          8192,
		Tools:              true,
		ParallelTools:      true,
		Thinking:           true,
		PrefixCache:        true,
		MinCacheTokens:     1024,
		RequiresAlternate:  true,
		ToolNamePattern:    `^[a-zA-Z0-9_-]{1,64}$`,
		MaxToolResultBytes: 32 * 1024,
	}
}

// Usable 返回基于该能力的可用输入预算：上下文窗口减最大输出与 10% 余量。
func (c Capabilities) Usable() int {
	window := c.ContextWindow
	if window <= 0 {
		window = 128000
	}
	reserve := c.MaxOutput
	if reserve <= 0 {
		reserve = 4096
	}
	budget := window - reserve - window/10
	return max(budget, 1024)
}
