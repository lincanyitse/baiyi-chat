package bootstrap

import (
	"log/slog"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/memory"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// memoryWiring 是记忆子系统的组件集合。
type memoryWiring struct {
	repo     *memory.Repo
	recaller *memory.Recaller
	writer   *memory.Writer
	extract  *memory.Extractor
	decayer  *memory.Decayer
	tools    *memory.Tools
}

// wireMemory 装配记忆子系统并注册工具。
//
// 记忆不依赖嵌入服务：抽取是纯规则，召回是纯 SQL。
// 这让它在「只配了对话模型」的最小配置下也能工作。
func wireMemory(db *sqlite.DB, logger *slog.Logger, toolReg *tool.Registry) *session.MemoryHooks {
	repo := memory.NewRepo(db.DB)
	recaller := memory.NewRecaller(repo, memory.RecallerOptions{
		ProfileTokenBudget: 300,
		MaxEpisodes:        3,
	})
	writer := memory.NewWriter(repo)
	extract := memory.NewExtractor()
	decayer := memory.NewDecayer(repo, memory.DecayerOptions{})
	tools := memory.NewTools(repo, recaller, writer)

	if err := toolReg.Register(tools.MemoryWriteTool()); err != nil {
		logger.Error("注册 memory_write 失败", "err", err)
	}
	if err := toolReg.Register(tools.MemorySearchTool()); err != nil {
		logger.Error("注册 memory_search 失败", "err", err)
	}

	logger.Info("记忆子系统就绪", "画像卡预算", 300, "情节召回上限", 3)

	return &session.MemoryHooks{
		Repo:     repo,
		Recaller: recaller,
		Writer:   writer,
		Extract:  extract,
		Episodic: true,
		Decayer:  decayer,
	}
}

// memoryAdminOf 把对话用的记忆组件收成管理接口。
//
// 返回接口而不是 *memory.Admin：nil 指针装进接口后 `opts.Memory == nil` 会失效。
func memoryAdminOf(h *session.MemoryHooks) web.MemoryAdmin {
	if h == nil || h.Repo == nil {
		return nil
	}
	return memory.NewAdmin(h.Repo, h.Recaller)
}
