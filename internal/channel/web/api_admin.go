package web

import (
	"net/http"
	"os"
	"sort"

	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// ProviderDTO 是对外的 Provider 描述。
type ProviderDTO struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// BaseURL 是完整地址。界面要能编辑它，脱敏会丢掉 /v1 这类路径，
	// 保存后配置就被改坏了。
	BaseURL string `json:"base_url"`
	// APIKeyEnv 是环境变量名，不是密钥本身——界面需要它才能编辑。
	APIKeyEnv string `json:"api_key_env"`
	// HasKey 报告加密存储里是否已存有密钥。不返回明文，也不返回
	// 脱敏后的前缀——长度与格式同样会泄露信息。
	HasKey bool `json:"has_key"`
	// KeySource 是实际生效的密钥来源：env / stored / none。
	KeySource string     `json:"key_source"`
	TimeoutS  int        `json:"timeout_s"`
	Models    []ModelDTO `json:"models"`
}

// ModelDTO 是模型能力声明。
//
// 字段必须与 config.Model 一一对应：少一个字段，界面读不到它，
// 保存时就会把用户手写的值静默清零。新增配置项时两处要同步。
type ModelDTO struct {
	ID            string  `json:"id"`
	ContextWindow int     `json:"context_window"`
	MaxOutput     int     `json:"max_output"`
	Tools         bool    `json:"tools"`
	Vision        bool    `json:"vision"`
	Thinking      bool    `json:"thinking"`
	PrefixCache   bool    `json:"prefix_cache"`
	StrictSchema  bool    `json:"strict_schema"`
	PriceIn       float64 `json:"price_in"`
	PriceOut      float64 `json:"price_out"`
	PriceCached   float64 `json:"price_cached"`
}

// handleListProviders 返回 Provider 与当前路由。
//
// 不返回 api_key：密钥只存在于环境变量，服务端从不读进内存以外的
// 地方，更不该经 HTTP 出站。
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	cfg := s.cfgProviders
	routesIn := s.cfgRoutes
	s.cfgMu.RUnlock()

	out := make([]ProviderDTO, 0, len(cfg))
	for name, p := range cfg {
		dto := ProviderDTO{
			Name:      name,
			Kind:      p.Kind,
			BaseURL:   p.BaseURL,
			APIKeyEnv: p.APIKeyEnv,
			TimeoutS:  p.TimeoutS,
		}

		// 密钥来源：环境变量优先，与 runtime 的解析顺序一致，
		// 否则界面会显示一个与实际情况不符的来源。
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) != "" {
			dto.KeySource = "env"
		}
		if s.opts.Secrets != nil {
			if has, err := s.opts.Secrets.Has(r.Context(), name); err == nil && has {
				dto.HasKey = true
				if dto.KeySource == "" {
					dto.KeySource = "stored"
				}
			}
		}
		if dto.KeySource == "" {
			dto.KeySource = "none"
		}

		for id, m := range p.Models {
			dto.Models = append(dto.Models, ModelDTO{
				ID:            id,
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
			})
		}
		out = append(out, dto)
	}

	// 路由是全局的，单独给一份 {task: [candidates]}，不挂在每个 Provider 上
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": out,
		"routes":    routeMapOf(routesIn),
	})
}

// handleListProviderModels 向 Provider 服务端查询可用模型列表。
//
// 只是「查询」不是「配置」：返回的模型不会自动加入配置——能力声明
// （上下文窗口、是否支持工具）只有用户知道，自动写入等于瞎猜。
// 界面用它做下拉候选，避免手抄模型 ID 抄错。
func (s *Server) handleListProviderModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	models, err := s.opts.Service.ListModels(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadGateway, ErrCodeUnavailable, err.Error())
		return
	}

	type modelDTO struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by,omitempty"`
	}
	out := make([]modelDTO, 0, len(models))
	for _, m := range models {
		out = append(out, modelDTO{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// handleListConfiguredModels 返回所有已配置模型的扁平列表。
//
// 与 /api/providers/{name}/models 的区别：那个查的是服务端有什么，
// 这个返回配置里声明过的——只有声明过的才有能力信息，才能被选用。
func (s *Server) handleListConfiguredModels(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	cfg := s.cfgProviders
	routesIn := s.cfgRoutes
	s.cfgMu.RUnlock()

	type configuredModel struct {
		Ref           string `json:"ref"` // "provider/model"
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		Vision        bool   `json:"vision"`
		Tools         bool   `json:"tools"`
		Thinking      bool   `json:"thinking"`
		ContextWindow int    `json:"context_window"`
	}

	out := make([]configuredModel, 0)
	names := make([]string, 0, len(cfg))
	for name := range cfg {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := cfg[name]
		ids := make([]string, 0, len(p.Models))
		for id := range p.Models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			m := p.Models[id]
			out = append(out, configuredModel{
				Ref:           name + "/" + id,
				Provider:      name,
				Model:         id,
				Vision:        m.Vision,
				Tools:         m.Tools,
				Thinking:      m.Thinking,
				ContextWindow: m.ContextWindow,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": out,
		"routes": routeMapOf(routesIn),
	})
}

// routeMapOf 把路由配置摊平成 {task: [candidates]}。
func routeMapOf(routes map[string]config.Route) map[string][]string {
	out := make(map[string][]string, len(routes))
	for task, route := range routes {
		out[task] = route.Candidates
	}
	return out
}

// handleCostReport 返回成本聚合。
func (s *Server) handleCostReport(w http.ResponseWriter, r *http.Request) {
	if s.opts.Ledger == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "账本不可用")
		return
	}

	now := s.opts.Now()
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "today"
	}

	// session 参数优先：用户在看某个会话时关心的是「这一场花了多少」
	if sessionID := r.URL.Query().Get("session"); sessionID != "" {
		if s.opts.SessionCost == nil {
			writeError(w, http.StatusNotFound, ErrCodeNotFound, "会话成本不可用")
			return
		}
		sum, err := s.opts.SessionCost(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session": sessionID,
			"calls":   sum.Calls,
			"input":   sum.Input,
			"output":  sum.Output,
			"cached":  sum.Cached,
			"cost":    sum.Cost,
		})
		return
	}

	from, ok := cost.Range(rangeName, now)
	if !ok {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "未知的时间范围: "+rangeName)
		return
	}

	rep, err := s.opts.Ledger.Report(r.Context(), from, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
