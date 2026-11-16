package web

import (
	"net/http"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// ProviderInput 是保存 Provider 的请求体。
//
// 不复用 config.Provider：它是内部类型，字段增删会直接影响协议。
// 这里只暴露界面真正能编辑的部分，api_key_env 是环境变量名而非密钥，
// 允许修改。
//
// Models 复用 ModelDTO：GET 与 PUT 描述的是同一组字段，分成两份
// 镜像结构体已经导致过「读接口漏字段 → 保存时被静默清零」的 bug。
type ProviderInput struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	// APIKey 是本次要写入加密存储的明文密钥。
	//
	// 留空表示「不修改已存的密钥」——界面永远不回显明文，
	// 所以不能靠「空串 = 清除」来判断：那会让用户每次保存都清掉密钥。
	// 要清除用 clear_key。
	APIKey   string            `json:"api_key"`
	ClearKey bool              `json:"clear_key"`
	TimeoutS int               `json:"timeout_s"`
	Models   []ModelDTO        `json:"models"`
	Headers  map[string]string `json:"headers"`
}

// ProvidersInput 是保存请求的整体结构。
type ProvidersInput struct {
	Providers []ProviderInput     `json:"providers"`
	Routes    map[string][]string `json:"routes"`
}

// handleSaveProviders 写回 Provider 配置并热重载。
func (s *Server) handleSaveProviders(w http.ResponseWriter, r *http.Request) {
	if s.opts.SaveProviders == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "当前模式不支持修改配置")
		return
	}

	var in ProvidersInput
	if !decodeJSON(w, r, &in) {
		return
	}

	providers := make(map[string]config.Provider, len(in.Providers))
	keys := make(map[string]string, len(in.Providers))
	for _, p := range in.Providers {
		if p.Name == "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "provider 缺少 name")
			return
		}
		if p.APIKey != "" && p.APIKeyEnv != "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
				"provider "+p.Name+" 不能同时填密钥与环境变量名，二选一")
			return
		}
		models := make(map[string]config.Model, len(p.Models))
		for _, m := range p.Models {
			if m.ID == "" {
				writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
					"provider "+p.Name+" 有模型缺少 id")
				return
			}
			models[m.ID] = config.Model{
				ContextWindow: m.ContextWindow,
				MaxOutput:     m.MaxOutput,
				Tools:         m.Tools,
				Vision:        m.Vision,
				Thinking:      m.Thinking,
				PrefixCache:   m.PrefixCache,
				StrictSchema:  m.StrictSchema,
				PriceIn:       m.PriceIn,
				PriceOut:      m.PriceOut,
				PriceCached:   m.PriceCached,
			}
		}
		providers[p.Name] = config.Provider{
			Kind:      p.Kind,
			BaseURL:   p.BaseURL,
			APIKeyEnv: p.APIKeyEnv,
			TimeoutS:  p.TimeoutS,
			Models:    models,
			Headers:   p.Headers,
		}

		switch {
		case p.ClearKey:
			keys[p.Name] = ""
		case p.APIKey != "":
			keys[p.Name] = p.APIKey
		}
	}

	routes := make(map[string]config.Route, len(in.Routes))
	for task, cands := range in.Routes {
		routes[task] = config.Route{Candidates: cands}
	}

	updated, err := s.opts.SaveProviders(providers, routes, keys)
	if err != nil {
		// 校验失败是用户输入问题，用 400；写盘失败才是 500
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	// 刷新本服务器的配置快照，否则下一次 GET 还会返回旧值
	s.cfgMu.Lock()
	s.cfgProviders = updated.Providers
	s.cfgRoutes = updated.Route
	s.cfgMu.Unlock()

	// 保存成功后返回新的 Provider 列表，前端不必再拉一次
	s.handleListProviders(w, r)
}
