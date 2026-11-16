// Package registry 根据配置构造 Provider 实例，并按任务类型路由。
package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/anthropic"
	"github.com/lincanyitse/baiyi-chat/internal/provider/openaicompat"
	"github.com/lincanyitse/baiyi-chat/internal/provider/responses"
)

// Registry 持有全部 Provider 与路由表。
type Registry struct {
	providers  map[string]llm.Provider
	routes     map[string][]string        // 任务类型 → ["provider/model", ...]
	configured map[string]map[string]bool // provider → 配置中声明的模型 ID
	health     *healthTracker
}

// ModelRef 是解析后的模型引用。
type ModelRef struct {
	Provider string
	Model    string
}

func (r ModelRef) String() string { return r.Provider + "/" + r.Model }

// Options 控制 Build 的行为。
type Options struct {
	// APIKey 解析某个 Provider 的密钥。nil 时退回环境变量。
	//
	// 做成回调而不是在 config 里加字段：config 是纯声明的配置，
	// 密钥的来源（环境变量 / 加密存储）属于运行时装配细节。
	APIKey func(name string, p config.Provider) string
}

// Build 根据配置构造全部 Provider。
func Build(cfg config.Config) (*Registry, error) {
	return BuildWithOptions(cfg, Options{})
}

// BuildWithOptions 根据配置与选项构造全部 Provider。
func BuildWithOptions(cfg config.Config, opts Options) (*Registry, error) {
	resolveKey := opts.APIKey
	if resolveKey == nil {
		resolveKey = func(_ string, p config.Provider) string { return p.ResolveAPIKey() }
	}

	reg := &Registry{
		providers:  make(map[string]llm.Provider, len(cfg.Providers)),
		routes:     make(map[string][]string, len(cfg.Route)),
		configured: make(map[string]map[string]bool, len(cfg.Providers)),
		health:     newHealthTracker(),
	}

	for name, pc := range cfg.Providers {
		caps := capabilityResolver(pc)
		models := make(map[string]bool, len(pc.Models))
		for id := range pc.Models {
			models[id] = true
		}
		reg.configured[name] = models
		key := resolveKey(name, pc)
		opts := anthropic.Options{
			Name:         name,
			BaseURL:      pc.BaseURL,
			APIKey:       key,
			Headers:      pc.Headers,
			Capabilities: caps,
		}

		switch pc.Kind {
		case "openai-compat":
			reg.providers[name] = openaicompat.New(openaicompat.Options{
				Name:         name,
				BaseURL:      pc.BaseURL,
				APIKey:       key,
				Headers:      pc.Headers,
				Capabilities: caps,
			})
		case "responses":
			reg.providers[name] = responses.New(responses.Options{
				Name:         name,
				BaseURL:      pc.BaseURL,
				APIKey:       key,
				Headers:      pc.Headers,
				Capabilities: caps,
			})
		case "anthropic":
			reg.providers[name] = anthropic.New(opts)
		default:
			return nil, fmt.Errorf("provider %q: 不支持的 kind %q", name, pc.Kind)
		}
	}

	for task, route := range cfg.Route {
		reg.routes[task] = append([]string(nil), route.Candidates...)
	}

	return reg, nil
}

func capabilityResolver(pc config.Provider) func(string) llm.Capabilities {
	return func(model string) llm.Capabilities {
		base := llm.DefaultOpenAICompat()
		if pc.Kind == "anthropic" {
			base = llm.DefaultAnthropic()
		}
		m, ok := pc.Models[model]
		if !ok {
			return base
		}
		if m.ContextWindow > 0 {
			base.ContextWindow = m.ContextWindow
		}
		if m.MaxOutput > 0 {
			base.MaxOutput = m.MaxOutput
		}
		base.Tools = m.Tools
		base.Vision = m.Vision
		base.Thinking = m.Thinking
		base.PrefixCache = m.PrefixCache
		base.StrictSchema = m.StrictSchema
		return base
	}
}

// ChatCandidates 返回配置中 chat 任务的路由候选（"provider/model" 字符串）。
// 供启动日志展示，不构造 Registry。
func ChatCandidates(cfg config.Config) ([]string, error) {
	r, ok := cfg.Route["chat"]
	if !ok {
		return nil, fmt.Errorf("配置缺少 route.chat")
	}
	return append([]string(nil), r.Candidates...), nil
}

// Names 返回全部 Provider 名称。
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

// Candidates 返回某任务类型的候选模型列表。
func (r *Registry) Candidates(task string) []ModelRef {
	raw, ok := r.routes[task]
	if !ok {
		raw, ok = r.routes["chat"]
		if !ok {
			return nil
		}
	}
	out := make([]ModelRef, 0, len(raw))
	for _, s := range raw {
		if ref, err := ParseRef(s); err == nil {
			out = append(out, ref)
		}
	}
	return out
}

// Resolve 为任务类型挑选第一个可用 Provider 与模型。
func (r *Registry) Resolve(task string) (llm.Provider, ModelRef, error) {
	for _, ref := range r.Candidates(task) {
		if p, ok := r.providers[ref.Provider]; ok {
			return p, ref, nil
		}
	}
	// 文案面向用户而非开发者：告诉他下一步做什么，而不是让他去
	// 找配置文件里的 route.chat.candidates。
	return nil, ModelRef{}, fmt.Errorf("还没有配置可用的模型服务。请到「设置 → 模型服务」里添加一个 Provider")
}

// Lookup 按名字取已注册的 Provider。
//
// 与会话级模型覆盖配套：Resolve 走任务路由，而用户明确指定了
// provider/model 时不该再经过路由。
func (r *Registry) Lookup(name string) (llm.Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// ListModels 查询某个 Provider 服务端的模型列表。
// Provider 不支持列举时返回错误。
func (r *Registry) ListModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error) {
	p, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("未配置 Provider %q", providerName)
	}
	lister, ok := p.(llm.ModelLister)
	if !ok {
		return nil, fmt.Errorf("provider %q 不支持列出模型", providerName)
	}
	return lister.ListModels(ctx)
}

// ConfiguredModels 返回某 Provider 在配置中声明过的模型 ID 集合。
func (r *Registry) ConfiguredModels(providerName string) map[string]bool {
	return r.configured[providerName]
}

// ParseRef 解析 "provider/model" 引用。模型名本身可能含斜杠，只按第一个斜杠切分。
func ParseRef(s string) (ModelRef, error) {
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || model == "" {
		return ModelRef{}, fmt.Errorf("模型引用格式应为 provider/model: %q", s)
	}
	return ModelRef{Provider: provider, Model: model}, nil
}
