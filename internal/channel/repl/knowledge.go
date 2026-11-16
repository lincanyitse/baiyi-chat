package repl

import (
	"context"

	"github.com/lincanyitse/baiyi-chat/internal/rag"
)

// Knowledge 是 REPL 需要的最小知识库能力。
//
// 用接口而不是直接收 *rag.Ingester：REPL 只用到四五个方法，
// 收具体类型会让所有测试都必须造出完整的嵌入后端与任务队列。
type Knowledge interface {
	IngestFile(ctx context.Context, path string) (rag.Document, error)
	Search(ctx context.Context, query string, limit int) (rag.SearchResult, error)
	ListDocuments(ctx context.Context, limit int) ([]rag.Document, error)
	DeleteDocument(ctx context.Context, id string) error
	EmbedDocument(ctx context.Context, documentID string) error
	RebuildEmbeddings(ctx context.Context, model string) (rag.RebuildCount, error)
	EmbedActive() bool
	EmbedModel() string
}

// knowledgeAdapter 把 rag 的三个组件拼成 REPL 需要的形状。
type knowledgeAdapter struct {
	ing      *rag.Ingester
	ret      *rag.Retriever
	repo     *rag.Repo
	hasEmbed bool
	model    string
}

// NewKnowledge 构造适配器。hasEmbed 表示是否配置了可用的嵌入后端。
func NewKnowledge(ing *rag.Ingester, ret *rag.Retriever, repo *rag.Repo, hasEmbed bool, model string) Knowledge {
	return &knowledgeAdapter{ing: ing, ret: ret, repo: repo, hasEmbed: hasEmbed, model: model}
}

func (k *knowledgeAdapter) IngestFile(ctx context.Context, path string) (rag.Document, error) {
	return k.ing.IngestFile(ctx, path)
}

func (k *knowledgeAdapter) Search(ctx context.Context, query string, limit int) (rag.SearchResult, error) {
	return k.ret.Search(ctx, query, limit)
}

func (k *knowledgeAdapter) ListDocuments(ctx context.Context, limit int) ([]rag.Document, error) {
	return k.repo.ListDocuments(ctx, limit)
}

func (k *knowledgeAdapter) DeleteDocument(ctx context.Context, id string) error {
	return k.repo.DeleteDocument(ctx, id)
}

func (k *knowledgeAdapter) EmbedDocument(ctx context.Context, documentID string) error {
	return k.ing.EmbedDocument(ctx, documentID)
}

func (k *knowledgeAdapter) RebuildEmbeddings(ctx context.Context, model string) (rag.RebuildCount, error) {
	return k.ing.RebuildEmbeddings(ctx, model)
}

func (k *knowledgeAdapter) EmbedActive() bool  { return k.hasEmbed }
func (k *knowledgeAdapter) EmbedModel() string { return k.model }
