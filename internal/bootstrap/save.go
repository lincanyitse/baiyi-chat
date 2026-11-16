package bootstrap

// 配置写回与热重载。
//
// 只允许改 Provider 与 Route：其余配置（数据目录、日志、会话预算）
// 改了对运行中的进程没有意义——它们要么在启动时就固化了，要么需要
// 重建整个装配。做成「保存后立即生效」反而是误导。

import (
	"context"
	"fmt"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// ErrNoConfigFile 表示没有可写回的配置文件。
var ErrNoConfigFile = fmt.Errorf("没有配置文件，无法写回")

// SaveProviders 写回 Provider 配置并热重载模型注册表。
//
// 顺序很关键：先把文件写成功，再重建 Registry。反过来的话，
// 新 Registry 可能基于一份没落盘的配置，重启后又变回去。
//
// 重建失败时文件已经落盘——这时把错误返回给调用方，但不要把
// 旧 Registry 换掉：运行中的服务还能继续用旧配置，用户改回去
// 再保存一次即可。
//
// keys 是本次要写入加密存储的明文密钥（provider 名 → 密钥）。
// 密钥先落库再重建 Registry：重建时要靠它构造 Provider。
// 空串表示删除该 Provider 的密钥。
//
// 返回写回后的完整配置，供调用方刷新自己的展示快照。
func (a *App) SaveProviders(providers map[string]config.Provider, routes map[string]config.Route, keys map[string]string) (config.Config, error) {
	if a.ConfigPath == "" {
		return config.Config{}, ErrNoConfigFile
	}

	cfg, err := config.SaveProviders(a.ConfigPath, providers, routes)
	if err != nil {
		return config.Config{}, err
	}

	// 密钥只进数据库，不进配置文件——配置文件可能被分享或提交进 git。
	// 写库失败时配置已经落盘，但密钥没存；返回错误让用户重试即可。
	if a.Secrets != nil {
		for name, key := range keys {
			if err := a.Secrets.Put(context.Background(), name, key); err != nil {
				return config.Config{}, fmt.Errorf("保存密钥失败: %w", err)
			}
		}
		// 被删除的 Provider 一并清掉密钥，避免残留。
		for name := range a.Config.Providers {
			if _, still := cfg.Providers[name]; still {
				continue
			}
			if err := a.Secrets.Delete(context.Background(), name); err != nil {
				a.Log.Warn("清理已删除 Provider 的密钥失败", "provider", name, "err", err)
			}
		}
	}

	// 用新配置重建 Registry。Build 会逐个校验 Provider 定义与路由目标，
	// 未通过时返回错误——这比事后才发现「模型不存在」要好。
	reg, err := a.buildRegistry(cfg)
	if err != nil {
		return config.Config{}, fmt.Errorf("新配置未通过校验（文件已写入，请修正后重试）: %w", err)
	}

	a.Config.Providers = cfg.Providers
	a.Config.Route = cfg.Route
	a.Service.SetRegistry(reg)

	a.Log.Info("Provider 配置已更新并热重载",
		"providers", len(cfg.Providers), "routes", len(cfg.Route))
	return cfg, nil
}

// ReloadSkills 重扫技能目录并刷新运行中的上下文。
//
// 只调 Service.ReloadSkills 是不够的：它刷新的是 L0 目录（system 里的
// 技能清单），而 Service.tools 是构造时复制的一份快照。技能启停不改变
// 工具集本身（load_skill 始终在），所以这里只需要刷新 L0。
func (a *App) ReloadSkills() error {
	return a.Service.ReloadSkills()
}

// SaveMCP 写回插件配置并热应用。
//
// 顺序：先应用（连上新的、断掉旧的），成功了再写文件。
// 反过来的话，配置已经落盘但连接没建起来，重启后用户会以为
// 保存没生效——而实际上是他填的 server 根本连不上。
//
// 连接失败不影响写盘：用户填错了命令，配置仍应保留，让他改了重试，
// 而不是每次保存都被打回。逐项错误放在返回的 map 里。
func (a *App) SaveMCP(ctx context.Context, servers []config.MCPServer) (config.Config, map[string]error, error) {
	if a.ConfigPath == "" {
		return config.Config{}, nil, ErrNoConfigFile
	}

	results := map[string]error{}
	if a.MCP != nil {
		results = a.MCP.Apply(ctx, servers)
	}

	cfg, err := config.SaveMCP(a.ConfigPath, servers)
	if err != nil {
		return config.Config{}, results, err
	}

	a.Config.MCP = cfg.MCP
	return cfg, results, nil
}
