package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/attach"
	"github.com/lincanyitse/baiyi-chat/internal/channel/web/stream"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/export"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/jobs"
	"github.com/lincanyitse/baiyi-chat/internal/rag"
	"github.com/lincanyitse/baiyi-chat/internal/secret"
	"github.com/lincanyitse/baiyi-chat/internal/skill"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// Options 配置 Web 服务。
type Options struct {
	Config  config.WebConfig
	Service Core
	Log     *slog.Logger
	Streams *stream.Manager
	// BaseContext 是后台轮次的父 ctx。不随 HTTP 请求取消——
	// 关闭标签页不该中断生成。
	BaseContext context.Context
	// Approver 是审批实现。为 nil 时 agent 会拒绝需要确认的调用。
	Approver tool.Approver
	// Approvals 是审批挂起表。为 nil 时不提供审批接口。
	Approvals *ApprovalBroker
	// Static 是嵌入的前端资源；nil 表示未构建前端，只提供 API。
	Static http.FileSystem
	// Version 用于健康检查展示。
	Version string

	// Providers 与 Routes 供管理面板展示与编辑（不含密钥）。
	// 保存成功后由 SaveProviders 的返回值刷新，因此这里只是初始快照。
	Providers map[string]config.Provider
	Routes    map[string]config.Route
	// Ledger 用于成本聚合。nil 表示成本面板不可用。
	Ledger *cost.Ledger
	// SessionCost 汇总单个会话的用量与费用。nil 表示不可用。
	SessionCost func(ctx context.Context, sessionID string) (cost.Summary, error)
	// Auth 是本地口令。nil 表示不启用认证。
	Auth *Auth
	// SaveProviders 写回 Provider 配置并热重载，返回生效后的配置。
	// keys 是本次要写入加密存储的明文密钥（provider 名 → 密钥，空串表示删除）。
	// nil 表示不可修改。
	SaveProviders func(providers map[string]config.Provider, routes map[string]config.Route, keys map[string]string) (config.Config, error)
	// Secrets 是加密存储，用于报告每个 Provider 是否已配置密钥。
	// nil 表示加密存储不可用（只能靠环境变量）。
	Secrets SecretStore
	// Skills 是技能目录。nil 表示未启用技能管理。
	Skills *skill.Catalog
	// ReloadSkills 重扫技能目录并刷新上下文里的 L0 目录。
	// 技能启停后必须调用，否则模型这一轮仍看得到被禁用的技能。
	ReloadSkills func() error
	// MCP 是插件运行时控制。nil 表示未启用插件管理。
	MCP MCPControl
	// SaveMCP 写回插件配置并热应用。nil 表示不可修改。
	SaveMCP MCPSaveFunc
	// MCPServers 是插件配置的初始快照。保存成功后由 SaveMCP 的返回值刷新。
	MCPServers []config.MCPServer

	// ExportSession 读取单个会话及其全部消息，供导出使用。
	ExportSession func(ctx context.Context, id string) (export.Doc, string, error)
	// ListSessionsForExport 返回全部会话 ID（不含已删除的）。
	ListSessionsForExport func(ctx context.Context) ([]string, error)
	// Backup 立即生成一份数据库快照。
	Backup func(ctx context.Context) (BackupFile, error)
	// ListBackups 列出已有备份。
	ListBackups func() ([]BackupFile, error)
	// DataDir / ConfigPath / LogFile 供「关于」页展示。
	DataDir    string
	ConfigPath string
	LogFile    string
	// Files 读文件内容，供诊断导出打包日志。nil 时诊断打包不可用。
	Files FileReader
	// ResetData 清空指定范围的数据，返回被删除行数的说明。
	// scope 是已经校验过的 "sessions" / "knowledge" / "all"。
	// 用字符串而不是本包的 ResetScope：装配层不该依赖渠道的类型。
	ResetData func(ctx context.Context, scope string) (map[string]int, error)
	// RAG 是知识库依赖。nil 表示未装配，相关接口返回 404。
	RAG *RAGDeps
	// Memory 是记忆管理。nil 表示未启用，相关接口返回未启用。
	Memory MemoryAdmin
	// Attachments 是附件存储。nil 表示未启用附件上传。
	Attachments *attach.Store
	// Now 可注入，便于测试时间相关的统计。
	Now func() time.Time
	// OnReady 在监听成功后调用，参数是访问 URL。
	//
	// 用回调而不是让 ListenAndServe 返回 URL：后者要阻塞到服务退出，
	// 返回值只能等关闭时才拿到，调用方没法在启动时用上它。
	OnReady func(url string)
}

// RAGDeps 打包知识库管理需要的依赖。
type RAGDeps struct {
	Repo      *rag.Repo
	Ingester  *rag.Ingester
	Retriever *rag.Retriever
	JobStore  *jobs.Store
}

// FileReader 读取文件内容，供诊断导出使用。
//
// 抽成接口是为了让测试能注入假文件系统，并让 web 层不直接依赖 os。
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// SecretStore 是密钥存储的接口，只暴露 Web 层需要的能力。
//
// 不直接用 *secret.Store：api 层不该能碰到数据密钥的细节，
// 也让测试可以换成内存实现。
type SecretStore interface {
	Put(ctx context.Context, providerName, apiKey string) error
	Get(ctx context.Context, providerName string) (string, bool, error)
	Has(ctx context.Context, providerName string) (bool, error)
	Delete(ctx context.Context, providerName string) error
	Reset(ctx context.Context) error
	// Protection 返回数据密钥的保护方式（dpapi / file-binding），
	// 供界面如实说明保护强度。
	Protection() secret.Protection
}

// Server 是 Web 渠道。
type Server struct {
	opts    Options
	mux     *http.ServeMux
	http    *http.Server
	startAt time.Time

	// cfgMu 保护 cfgProviders/cfgRoutes/cfgMCP：保存配置会替换它们，
	// 而读接口可能同时在跑。
	cfgMu        sync.RWMutex
	cfgProviders map[string]config.Provider
	cfgRoutes    map[string]config.Route
	cfgMCP       []config.MCPServer
}

// New 构造 Web 服务。
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Streams == nil {
		opts.Streams = stream.NewManager(stream.ManagerOptions{})
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	s := &Server{
		opts:         opts,
		startAt:      time.Now(),
		cfgProviders: opts.Providers,
		cfgRoutes:    opts.Routes,
		cfgMCP:       opts.MCPServers,
	}
	s.mux = s.routes()
	s.http = &http.Server{
		Addr:    opts.Config.Listen,
		Handler: s.middleware(s.requireAuth(s.mux)),
		// 不设 WriteTimeout：SSE 是长连接，写超时会把它掐断。
		// 只限制读头部与空闲，避免慢速攻击。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe 启动服务并阻塞。ctx 取消时优雅关闭。
func (s *Server) ListenAndServe(ctx context.Context) error {
	if !s.opts.Config.IsLoopback() {
		s.opts.Log.Warn("Web 服务监听在非回环地址，局域网内其他设备可访问",
			"listen", s.opts.Config.Listen)
	}

	ln, err := net.Listen("tcp", s.opts.Config.Listen)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.opts.Config.Listen, err)
	}
	addr := ln.Addr().String()
	url := "http://" + addr
	s.opts.Log.Info("Web 服务已启动", "addr", addr, "url", url)
	if s.opts.OnReady != nil {
		s.opts.OnReady(url)
	}

	// 定期清理过期的轮次日志，防止内存随会话数增长
	go s.sweepLoop(ctx)

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// 先取消在飞请求（SSE 连接），再等 Serve 退出
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			_ = s.http.Close()
			return err
		}
		return nil
	}
}

func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.opts.Streams.Sweep(time.Now()); n > 0 {
				s.opts.Log.Debug("清理过期轮次日志", "count", n)
			}
		}
	}
}

// Handler 返回处理器，供测试直接使用。
func (s *Server) Handler() http.Handler { return s.http.Handler }

// routes 构建路由表。
//
// 用 Go 1.22 的方法+路径模式，不引第三方路由库——路由条数在几十个
// 量级，标准库完全够用。
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// 健康检查
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// 会话
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handleUpdateSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleMessages)
	mux.HandleFunc("GET /api/sessions/{id}/tree", s.handleMessageTree)
	mux.HandleFunc("GET /api/sessions/{id}/search", s.handleSearchMessages)
	mux.HandleFunc("GET /api/sessions/{id}/context", s.handleSessionContexts)

	// 对话
	mux.HandleFunc("POST /api/sessions/{id}/turns", s.handleStartTurn)
	mux.HandleFunc("GET /api/turns/{id}/events", s.handleTurnEvents)
	mux.HandleFunc("GET /api/turns/{id}", s.handleTurnStatus)
	mux.HandleFunc("GET /api/turns/{id}/context", s.handleTurnContext)
	mux.HandleFunc("GET /api/turns/{id}/context/diff", s.handleTurnContextDiff)
	mux.HandleFunc("POST /api/turns/{id}/stop", s.handleStopTurn)
	mux.HandleFunc("POST /api/turns/{id}/resume", s.handleResumeTurn)

	// 分支
	mux.HandleFunc("POST /api/messages/{id}/regenerate", s.handleRegenerate)
	mux.HandleFunc("PATCH /api/messages/{id}", s.handleEditMessage)
	mux.HandleFunc("GET /api/messages/{id}/siblings", s.handleSiblings)

	// 审批
	mux.HandleFunc("GET /api/approvals/pending", s.handlePendingApprovals)
	mux.HandleFunc("GET /api/approvals/stream", s.handleApprovalStream)
	mux.HandleFunc("POST /api/approvals/{id}", s.handleApprovalDecision)

	// 管理（只读）
	mux.HandleFunc("GET /api/providers", s.handleListProviders)
	mux.HandleFunc("GET /api/providers/{name}/models", s.handleListProviderModels)
	mux.HandleFunc("GET /api/models", s.handleListConfiguredModels)
	mux.HandleFunc("GET /api/cost", s.handleCostReport)
	// 管理（可写）
	mux.HandleFunc("PUT /api/providers", s.handleSaveProviders)
	mux.HandleFunc("POST /api/providers/secrets/reset", s.handleResetSecrets)
	mux.HandleFunc("GET /api/providers/secrets/status", s.handleSecretsStatus)

	// 技能
	mux.HandleFunc("GET /api/skills", s.handleListSkills)
	mux.HandleFunc("PATCH /api/skills/{name}", s.handleSetSkillEnabled)
	mux.HandleFunc("POST /api/skills/reload", s.handleReloadSkills)

	// 插件（MCP）
	mux.HandleFunc("GET /api/mcp", s.handleListMCP)
	mux.HandleFunc("PUT /api/mcp", s.handleSaveMCP)
	mux.HandleFunc("POST /api/mcp/{alias}/reconnect", s.handleReconnectMCP)

	// 数据出口
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleExportSession)
	mux.HandleFunc("GET /api/export", s.handleExportAll)
	mux.HandleFunc("POST /api/backup", s.handleCreateBackup)
	mux.HandleFunc("GET /api/backups", s.handleListBackups)

	// 系统信息与诊断
	mux.HandleFunc("GET /api/system", s.handleSystemInfo)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("POST /api/reset", s.handleResetData)

	// 记忆
	mux.HandleFunc("GET /api/memory/facts", s.handleListFacts)
	mux.HandleFunc("PATCH /api/memory/facts/{id}", s.handleFactAction)
	mux.HandleFunc("GET /api/memory/facts/{id}/events", s.handleFactEvents)
	mux.HandleFunc("GET /api/memory/card", s.handleProfileCard)
	mux.HandleFunc("GET /api/memory/stats", s.handleMemoryStats)

	// 知识库
	mux.HandleFunc("GET /api/kb/documents", s.handleListDocuments)
	mux.HandleFunc("POST /api/kb/documents", s.handleUploadDocument)
	mux.HandleFunc("DELETE /api/kb/documents/{id}", s.handleDeleteDocument)
	mux.HandleFunc("GET /api/kb/documents/{id}/chunks", s.handleDocumentChunks)
	mux.HandleFunc("POST /api/kb/search", s.handleKBSearch)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)

	// 附件
	mux.HandleFunc("POST /api/attachments", s.handleUploadAttachment)
	mux.HandleFunc("GET /api/attachments/{id}", s.handleGetAttachment)

	// 认证
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/auth/setup", s.handleSetupPassword)
	mux.HandleFunc("POST /api/auth/change", s.handleChangePassword)

	// 静态资源
	if s.opts.Static != nil {
		mux.Handle("GET /", s.spaHandler())
	}

	return mux
}

// middleware 是中间件链：恢复 panic → 日志 → 安全头。
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 恢复 panic：一个 handler 出错不该带走整个服务
		defer func() {
			if rec := recover(); rec != nil {
				s.opts.Log.Error("HTTP handler panic",
					"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(rec))
				writeError(w, http.StatusInternalServerError, ErrCodeInternal, "服务内部错误")
			}
		}()

		// 安全头。CSP 限制到 self，前端不引外部资源。
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}

		// 跨站防护：变更类请求校验 Origin。
		// SameSite Cookie 已经挡了一层，这里是第二道——
		// 不依赖 Cookie 的客户端（curl）没有 Origin，放行。
		if isMutating(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
				writeError(w, http.StatusForbidden, ErrCodeUnauthorized, "跨站请求被拒绝")
				return
			}
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// SSE 是长连接，记录耗时没有意义，只记非流式请求
		if !strings.HasPrefix(r.URL.Path, "/api/turns/") || !strings.HasSuffix(r.URL.Path, "/events") {
			s.opts.Log.Debug("HTTP",
				"method", r.Method, "path", r.URL.Path,
				"status", rec.status, "dur_ms", time.Since(start).Milliseconds())
		}
	})
}

func isMutating(method string) bool {
	return method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete
}

// sameOrigin 判断请求来源是否与自身同源。
func sameOrigin(origin, host string) bool {
	origin = strings.TrimSuffix(origin, "/")
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(origin, scheme) {
			return strings.TrimPrefix(origin, scheme) == host
		}
	}
	return false
}

// statusRecorder 记录响应状态码供日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	// written 防止重复 WriteHeader
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.written {
		return
	}
	r.written = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

// Flush 让 statusRecorder 也满足 http.Flusher，SSE 才能工作。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.opts.Streams.Stats()
	writeJSON(w, http.StatusOK, HealthDTO{
		Status:        "ok",
		Version:       s.opts.Version,
		UptimeSec:     int64(time.Since(s.startAt).Seconds()),
		StreamTurns:   st.Turns,
		StreamEntries: st.Entries,
		StreamBytes:   st.Bytes,
	})
}

// spaHandler 提供前端静态资源，未命中的路径回退到 index.html。
//
// 回退是 SPA 的前提：/s/abc123 这类前端路由在服务端没有对应文件，
// 必须交给 index.html 里的客户端路由处理。
func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServer(s.opts.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, ErrCodeNotFound, "接口不存在")
			return
		}
		f, err := s.opts.Static.Open(strings.TrimPrefix(r.URL.Path, "/"))
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// 未命中的路径交给 SPA
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写统一错误响应。
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, ErrorResponse{Error: ErrorBody{Code: code, Message: msg}})
}

// decodeJSON 解析请求体，限制大小防止恶意请求耗尽内存。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "请求体格式错误: "+err.Error())
		return false
	}
	return true
}

// maxUploadSize 是表单上传的单文件上限。
const maxUploadSize = 8 << 20

// readUpload 读取 multipart 表单里的 file 字段。
//
// 知识库上传与附件上传共用这一套：大小限制、表单解析、缺失字段、
// 空文件，四道错误分支的文案与状态码必须一致，各写一份只会分叉。
func readUpload(w http.ResponseWriter, r *http.Request) (data []byte, name string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, ErrCodeBadRequest,
			"文件过大或表单格式有误（上限 8 MiB）")
		return nil, "", false
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "缺少 file 字段")
		return nil, "", false
	}
	defer file.Close()

	data, err = io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "读取文件失败: "+err.Error())
		return nil, "", false
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "文件为空")
		return nil, "", false
	}
	return data, header.Filename, true
}
