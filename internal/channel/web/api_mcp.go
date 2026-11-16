package web

import (
	"context"
	"net/http"
	"sort"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// MCPInput 是保存插件配置的请求体。
//
// 不复用 config.MCPServer：它是内部类型，字段增删会直接影响协议。
type MCPInput struct {
	Alias     string   `json:"alias"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	URL       string   `json:"url"`
	// AllowedTools 留空表示全部允许。
	AllowedTools []string `json:"allowed_tools"`
	// Risk 是 read / write / dangerous，留空按 read。
	Risk    string `json:"risk"`
	Enabled bool   `json:"enabled"`
}

// MCPDTO 是对外的插件描述。
type MCPDTO struct {
	Alias        string   `json:"alias"`
	Transport    string   `json:"transport"`
	Command      string   `json:"command"`
	Args         []string `json:"args"`
	URL          string   `json:"url"`
	AllowedTools []string `json:"allowed_tools"`
	Risk         string   `json:"risk"`
	Enabled      bool     `json:"enabled"`

	// 运行时状态
	Connected bool     `json:"connected"`
	LastError string   `json:"last_error"`
	Tools     []string `json:"tools"`
}

// MCPResult 是保存后每个 server 的连接结果。
type MCPResult struct {
	Alias   string `json:"alias"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// MCPControl 是插件运行时的控制接口，由 bootstrap 的 MCPManager 实现。
type MCPControl interface {
	Status() map[string]ServerStatus
	Reconnect(ctx context.Context, alias string) error
}

// ServerStatus 是插件的连接状态。
type ServerStatus struct {
	Connected bool
	LastError string
	Tools     []string
}

// MCPSaveFunc 保存并应用插件配置。返回逐项结果，而非整体成败——
// 一个 server 连不上不该让整次保存失败。
type MCPSaveFunc func(ctx context.Context, servers []config.MCPServer) (config.Config, map[string]error, error)

// handleListMCP 返回插件列表与运行状态。
func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCP == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "插件管理未启用")
		return
	}

	s.cfgMu.RLock()
	servers := s.cfgMCP
	s.cfgMu.RUnlock()

	status := s.opts.MCP.Status()

	out := make([]MCPDTO, 0, len(servers))
	for _, sc := range servers {
		dto := MCPDTO{
			Alias:        sc.Alias,
			Transport:    sc.Transport,
			Command:      sc.Command,
			Args:         sc.Args,
			URL:          sc.URL,
			AllowedTools: sc.AllowedTools,
			Risk:         sc.Risk,
			Enabled:      sc.Enabled,
			Tools:        []string{},
		}
		if st, ok := status[sc.Alias]; ok {
			dto.Connected = st.Connected
			dto.LastError = st.LastError
			if len(st.Tools) > 0 {
				dto.Tools = st.Tools
			}
		}
		out = append(out, dto)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })

	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// handleSaveMCP 写回插件配置并热应用。
func (s *Server) handleSaveMCP(w http.ResponseWriter, r *http.Request) {
	if s.opts.SaveMCP == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "当前模式不支持修改插件配置")
		return
	}

	var in struct {
		Servers []MCPInput `json:"servers"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	servers := make([]config.MCPServer, 0, len(in.Servers))
	seen := make(map[string]bool, len(in.Servers))
	for _, srv := range in.Servers {
		if srv.Alias == "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "插件缺少 alias")
			return
		}
		if seen[srv.Alias] {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
				"alias "+srv.Alias+" 重复")
			return
		}
		seen[srv.Alias] = true

		switch srv.Transport {
		case "stdio":
			if srv.Command == "" {
				writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
					"插件 "+srv.Alias+" 使用 stdio 时必须填命令")
				return
			}
		case "http", "streamable-http", "":
			if srv.URL == "" {
				writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
					"插件 "+srv.Alias+" 使用 http 时必须填地址")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
				"插件 "+srv.Alias+" 的传输方式未知: "+srv.Transport)
			return
		}

		servers = append(servers, config.MCPServer{
			Alias:        srv.Alias,
			Transport:    srv.Transport,
			Command:      srv.Command,
			Args:         srv.Args,
			URL:          srv.URL,
			AllowedTools: srv.AllowedTools,
			Risk:         srv.Risk,
			Enabled:      srv.Enabled,
		})
	}

	updated, results, err := s.opts.SaveMCP(r.Context(), servers)
	if err != nil {
		// 写盘失败才是整体失败；连接失败放在 results 里逐项报
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	s.cfgMu.Lock()
	s.cfgMCP = updated.MCP
	s.cfgMu.Unlock()

	// 逐项结果按 alias 排序，保证多次保存的响应稳定
	out := make([]MCPResult, 0, len(results))
	for alias, e := range results {
		res := MCPResult{Alias: alias, OK: e == nil}
		if e != nil {
			res.Message = e.Error()
		}
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })

	// 返回 DTO 而不是 config.MCPServer：后者没有 json tag，
	// 序列化出来是 Go 的大驼峰字段名，与 GET /api/mcp 的协议不一致。
	dtos := make([]MCPDTO, 0, len(updated.MCP))
	for _, sc := range updated.MCP {
		dtos = append(dtos, MCPDTO{
			Alias:        sc.Alias,
			Transport:    sc.Transport,
			Command:      sc.Command,
			Args:         sc.Args,
			URL:          sc.URL,
			AllowedTools: sc.AllowedTools,
			Risk:         sc.Risk,
			Enabled:      sc.Enabled,
			Tools:        []string{},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"servers": dtos,
		"results": out,
	})
}

// handleReconnectMCP 单体重连。
func (s *Server) handleReconnectMCP(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCP == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "插件管理未启用")
		return
	}
	alias := r.PathValue("alias")
	if err := s.opts.MCP.Reconnect(r.Context(), alias); err != nil {
		writeError(w, http.StatusBadGateway, ErrCodeUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
