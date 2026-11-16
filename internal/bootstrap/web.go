package bootstrap

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lincanyitse/baiyi-chat/internal/attach"
	"github.com/lincanyitse/baiyi-chat/internal/channel/web"
	"github.com/lincanyitse/baiyi-chat/internal/secret"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// RunWeb 启动 Web 服务并阻塞，直到 ctx 取消。
func (a *App) RunWeb(ctx context.Context) error {
	static, ok := web.Static()
	if !ok {
		a.Log.Warn("未找到前端构建产物 web/dist，仅提供 API。" +
			"运行 make web 构建前端后重新启动。")
	}

	// 审批挂起表：Web 用弹窗，REPL 仍走终端 y/n。
	// 只在 Web 渠道创建，避免 REPL 场景下内存里挂一堆没人看的请求。
	broker := web.NewApprovalBroker(func(req web.ApprovalRequest, d tool.ApprovalDecision) {
		a.Log.Info("工具审批已处理",
			"turn", req.TurnID, "tool", req.ToolName,
			"action", string(d.Action), "reason", d.Reason)
	})

	// 只读模式：显式配置时把写类与危险工具从模型视野里摘掉。
	// 默认关闭（审批弹窗已就绪），但保留这个开关作为最小授权兜底。
	a.applyToolPolicy(broker)

	// 口令：只在有数据库时启用；未设置口令时 requireAuth 直接放行。
	auth := web.NewAuth(a.DB.DB)

	// 知识库依赖：未配置嵌入时这些仍然可用（检索退化为关键词）
	var ragDeps *web.RAGDeps
	if a.RAGRepo != nil {
		ragDeps = &web.RAGDeps{
			Repo:      a.RAGRepo,
			Ingester:  a.Ingester,
			Retriever: a.Retriever,
			JobStore:  a.JobStore,
		}
	}

	// 附件存储：内容寻址，与 workspace 平级
	attachStore := attach.NewStore(filepath.Join(a.Config.DataDir, "attachments"))

	srv := web.New(web.Options{
		Config:                a.Config.Web,
		Service:               a.Service,
		Log:                   a.Log,
		Streams:               a.Streams,
		BaseContext:           ctx,
		Approver:              broker,
		Approvals:             broker,
		Static:                static,
		Version:               a.Version,
		Providers:             a.Config.Providers,
		Routes:                a.Config.Route,
		Ledger:                a.Ledger,
		SessionCost:           a.SessionCost,
		Auth:                  auth,
		SaveProviders:         a.SaveProviders,
		RAG:                   ragDeps,
		Memory:                memoryAdminOf(a.Memory),
		Attachments:           attachStore,
		OnReady:               a.OnWebReady,
		Secrets:               secretStoreOf(a.Secrets),
		Skills:                a.Skills,
		ReloadSkills:          a.ReloadSkills,
		MCP:                   mcpControlOf(a.MCP),
		SaveMCP:               a.SaveMCP,
		MCPServers:            a.Config.MCP,
		ExportSession:         a.ExportSession,
		ListSessionsForExport: a.ListSessionsForExport,
		Backup:                a.backupFileOf,
		ListBackups:           a.backupFilesOf,
		DataDir:               a.Config.DataDir,
		ConfigPath:            a.ConfigPath,
		LogFile:               a.LogFilePath(),
		Files:                 osFileReader{},
		ResetData:             a.ResetData,
	})
	return srv.ListenAndServe(ctx)
}

// secretStoreOf 把 *secret.Store 转成接口，nil 时返回真正的 nil 接口。
//
// 直接赋值会让一个 nil 指针包进非 nil 接口，`opts.Secrets == nil`
// 判不出来，handler 会对着空指针调用而 panic。
func secretStoreOf(s *secret.Store) web.SecretStore {
	if s == nil {
		return nil
	}
	return s
}

// mcpControlOf 同理：nil 指针包进接口会让 `opts.MCP == nil` 失效。
func mcpControlOf(m *MCPManager) web.MCPControl {
	if m == nil {
		return nil
	}
	return mcpControl{m}
}

// mcpControl 把 bootstrap 的 ServerStatus 适配成 web 层的那一份。
//
// 两个包各有一份同构状态：bootstrap 的是内部视图（含传输、风险），
// Web 只需要连接状态。用适配器而不是让 web 反向依赖 bootstrap 的内部结构。
type mcpControl struct{ m *MCPManager }

func (c mcpControl) Status() map[string]web.ServerStatus {
	out := make(map[string]web.ServerStatus)
	for alias, st := range c.m.Status() {
		out[alias] = web.ServerStatus{
			Connected: st.Connected,
			LastError: st.LastError,
			Tools:     st.Tools,
		}
	}
	return out
}

func (c mcpControl) Reconnect(ctx context.Context, alias string) error {
	return c.m.Reconnect(ctx, alias)
}

// backupFileOf / backupFilesOf 把 bootstrap 的 BackupInfo 转成 web 层的类型。
//
// 两个包各有一份同构结构：web 不该依赖 bootstrap（分层方向），
// bootstrap 也不该为了 Web 的需求改自己的内部类型。
func (a *App) backupFileOf(ctx context.Context) (web.BackupFile, error) {
	info, err := a.Backup(ctx)
	if err != nil {
		return web.BackupFile{}, err
	}
	return web.BackupFile{Path: info.Path, Size: info.Size, ModTime: info.ModTime}, nil
}

func (a *App) backupFilesOf() ([]web.BackupFile, error) {
	all, err := a.ListBackups()
	if err != nil {
		return nil, err
	}
	out := make([]web.BackupFile, 0, len(all))
	for _, b := range all {
		out = append(out, web.BackupFile{Path: b.Path, Size: b.Size, ModTime: b.ModTime})
	}
	return out, nil
}

// osFileReader 让 web 层的诊断导出能读文件，而不必 import os。
type osFileReader struct{}

func (osFileReader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// LogFilePath 返回日志文件位置，供「关于」页与诊断导出使用。
func (a *App) LogFilePath() string {
	if a.Config.Log.File != "" {
		return a.Config.Log.File
	}
	return filepath.Join(a.Config.DataDir, "logs", "baiyi.log")
}

// applyToolPolicy 按配置收窄工具集。
//
// 从 session.Service 的视角「禁用」就是把工具从列表里移除：
// agent 拿到的 Tools 里没有它，模型看不到，也就不会去调。
func (a *App) applyToolPolicy(broker *web.ApprovalBroker) {
	if !a.Config.Web.ReadOnlyTools {
		a.Log.Info("工具审批已启用", "approver", "web", "broker", broker != nil)
		return
	}

	tools := a.Service.CurrentTools()

	kept := make([]tool.Tool, 0, len(tools))
	disabled := make([]string, 0)
	for _, t := range tools {
		if t.Spec().Risk == tool.RiskRead {
			kept = append(kept, t)
			continue
		}
		disabled = append(disabled, t.Spec().Name)
	}
	a.Service.SetTools(kept)
	a.Log.Info("只读模式已启用，写类工具不下发给模型", "disabled", disabled)
}
