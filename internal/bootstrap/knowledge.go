package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/jobs"
	"github.com/lincanyitse/baiyi-chat/internal/rag"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// knowledge 是 M2 知识库的全部组件。
type knowledge struct {
	repo      *rag.Repo
	ingester  *rag.Ingester
	retriever *rag.Retriever
	embedder  embed.Embedder
	reranker  embed.Reranker
	jobs      *jobs.Store
	runner    *jobs.Runner
	cancel    context.CancelFunc
}

// wireKnowledge 装配知识库：嵌入后端、仓储、解析器、切分器、检索器、任务队列。
//
// 关键取舍：嵌入服务不可用时**不报错**。知识库仍以关键词检索工作，
// 用户上传文档、搜索资料都正常，只是召回质量差一些。等到用户配好
// 嵌入服务，重建一次向量即可——旧数据一行没丢。
func wireKnowledge(
	ctx context.Context, cfg config.Config, db *sqlite.DB,
	logger *slog.Logger, toolReg *tool.Registry,
) (*knowledge, error) {
	embedder, err := embed.Build(embedSettings(cfg.Embed))
	if err != nil {
		return nil, err
	}
	reranker, err := embed.BuildReranker(rerankSettings(cfg.Rerank))
	if err != nil {
		return nil, err
	}

	// 独立的 Web 重排后端优先；否则嵌入后端自带的那个。
	//
	// Limiter 实现了 Reranker，但重排后端为 nil 时调用会返回
	// ErrNotConfigured。所以要先问能力，不能直接断言接口——
	// 断言成功不代表背后真有重排实现。
	if reranker == nil && embedder != nil {
		if r, ok := embedder.(interface{ HasReranker() bool }); ok && r.HasReranker() {
			reranker = embedder.(embed.Reranker)
		}
	}

	repo := rag.NewRepo(db.DB)
	jobStore := jobs.NewStore(db.DB)
	runner := jobs.NewRunner(jobStore, jobs.RunnerOptions{
		Workers: 1, // SQLite 单写者，开多并发只会制造锁等待
		Logger:  logger,
	})

	// 崩溃恢复：上次进程留下的 running 任务放回队列。
	// 阈值取 10 分钟——比任何单任务超时都短，不会误伤正在跑的任务
	// （那些任务的 started_at 是刚才更新的）。
	if n, err := jobStore.RequeueStale(ctx, 10*time.Minute); err != nil {
		logger.Warn("回收陈旧任务失败", "err", err)
	} else if n > 0 {
		logger.Info("回收了上次未完成的任务", "count", n)
	}

	parsers := rag.NewParserRegistry()
	ingester := rag.NewIngester(repo, parsers, rag.IngesterOptions{
		Model:   cfg.Embed.Model,
		Target:  cfg.RAG.ChunkTokens,
		Overlap: cfg.RAG.ChunkOverlap,
		Queue:   jobStore,
		Runner:  runner,
		Log:     slogAdapter{logger},
	})
	ingester.SetEmbedder(embedder)

	retriever := rag.NewRetriever(repo, embedder, reranker, rag.RetrieverOptions{
		TopKVector:   cfg.RAG.TopKVector,
		TopKKeyword:  cfg.RAG.TopKKeyword,
		TopKRerank:   cfg.RAG.TopKRerank,
		EnableRerank: cfg.RAG.EnableRerank,
		Model:        cfg.Embed.Model,
	})

	// 写完向量立刻让检索缓存失效，否则用户 /kb add 后要等缓存过期才搜得到。
	ingester.OnVectorsChanged(retriever.Invalidate)

	if err := toolReg.Register(rag.NewSearchTool(retriever)); err != nil {
		return nil, err
	}
	if err := toolReg.Register(rag.NewDocumentListTool(repo)); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runner.Start(runCtx)

	logger.Info("知识库就绪",
		"embed", describeEmbed(cfg.Embed),
		"rerank", describeRerank(cfg.Rerank, reranker),
		"chunk_tokens", cfg.RAG.ChunkTokens)

	return &knowledge{
		repo:      repo,
		ingester:  ingester,
		retriever: retriever,
		embedder:  embedder,
		reranker:  reranker,
		jobs:      jobStore,
		runner:    runner,
		cancel:    cancel,
	}, nil
}

func describeEmbed(c config.EmbedConfig) string {
	if !c.Enabled() {
		return "未配置（检索退化为关键词）"
	}
	return c.Kind + "/" + c.Model
}

func describeRerank(c config.RerankConfig, r embed.Reranker) string {
	if r == nil {
		return "未配置（按融合分排序）"
	}
	if c.Kind != "" && c.Kind != "none" {
		return c.Kind + "/" + c.Model
	}
	return "随嵌入后端"
}

// slogAdapter 让 *slog.Logger 满足 rag 的最小日志接口，
// 避免 rag 依赖 slog 的具体签名。
type slogAdapter struct{ l *slog.Logger }

func (s slogAdapter) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogAdapter) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogAdapter) Error(msg string, args ...any) { s.l.Error(msg, args...) }

// embedSettings 把配置翻译成 embed.Settings。
//
// 翻译放在 bootstrap 而不是 embed 内：embed 是中立能力包，
// 一旦它 import config，配置加载（koanf、文件系统、环境变量）
// 就进了每个用到嵌入的包的依赖闭包。
func embedSettings(c config.EmbedConfig) embed.Settings {
	return embed.Settings{
		Kind:            c.Kind,
		BaseURL:         c.BaseURL,
		APIKeyENV:       c.APIKeyEnv,
		Model:           c.Model,
		Dims:            c.Dims,
		BatchSize:       c.BatchSize,
		RPS:             c.RPS,
		Timeout:         time.Duration(c.TimeoutS) * time.Second,
		Method:          c.Method,
		Headers:         c.Headers,
		RequestTemplate: c.RequestTemplate,
		ResponsePath:    c.ResponsePath,
		RerankPath:      c.RerankPath,
	}
}

func rerankSettings(c config.RerankConfig) embed.RerankSettings {
	return embed.RerankSettings{
		Kind:      c.Kind,
		BaseURL:   c.BaseURL,
		APIKeyENV: c.APIKeyEnv,
		Model:     c.Model,
		Path:      c.Path,
		RPS:       c.RPS,
		Timeout:   time.Duration(c.TimeoutS) * time.Second,
	}
}
