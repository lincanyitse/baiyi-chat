package rag

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
	"github.com/lincanyitse/baiyi-chat/internal/jobs"
)

// 任务类型。
//
// 只有「补向量」和「全量重建」进队列：解析与切分是本地毫秒级操作，
// 同步做完再入队，用户能立刻在文档列表里看到结果。
const (
	JobEmbed   = "rag.embed"   // 为一篇文档补向量
	JobRebuild = "rag.rebuild" // 全量重建向量
)

// Ingester 负责把文件接入知识库。
type Ingester struct {
	repo     *Repo
	parsers  *ParserRegistry
	chunker  *Chunker
	queue    *jobs.Store
	runner   *jobs.Runner
	embedder embed.Embedder
	model    string
	log      logger

	// onVectorsChanged 在写入向量后回调，用于让检索缓存失效。
	onVectorsChanged func()
}

// logger 只取需要的方法，避免把 slog 的类型渗透到每个调用点。
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// IngesterOptions 配置接入器。
type IngesterOptions struct {
	Model   string
	Target  int
	Overlap float64
	Queue   *jobs.Store
	Runner  *jobs.Runner
	Log     logger
}

// NewIngester 构造接入器并注册 handler。
func NewIngester(repo *Repo, parsers *ParserRegistry, opts IngesterOptions) *Ingester {
	in := &Ingester{
		repo:    repo,
		parsers: parsers,
		chunker: NewChunker(opts.Target, opts.Overlap),
		queue:   opts.Queue,
		runner:  opts.Runner,
		model:   opts.Model,
		log:     opts.Log,
	}
	if in.model == "" {
		in.model = "unknown"
	}
	if in.log == nil {
		in.log = nopLogger{}
	}
	if opts.Runner != nil {
		opts.Runner.Register(embedHandler{in: in})
		opts.Runner.Register(rebuildHandler{in: in})
	}
	return in
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// IngestFile 读取并接入一个本地文件。
//
// 解析与切分是同步的（本地 CPU、毫秒级），嵌入是异步的（网络、秒级）。
// 先落库再入队，用户能立刻在文档列表里看到它，进度也能查。
func (in *Ingester) IngestFile(ctx context.Context, path string) (Document, error) {
	return in.ingestNow(ctx, path)
}

// IngestBytes 接入内存中的内容（用于网页抓取、剪贴板、Web 上传）。
//
// mime 为空或是通用二进制类型时按扩展名推断：浏览器与 curl 常常
// 送 application/octet-stream，直接采信会让解析器选不到正确分支。
func (in *Ingester) IngestBytes(ctx context.Context, name, mime string, data []byte) (Document, error) {
	if mime == "" || mime == "application/octet-stream" {
		if guessed := MIMEByExt(name); guessed != "" {
			mime = guessed
		}
	}
	return in.ingestData(ctx, name, mime, data)
}

// ingestNow 同步完成解析与切分。
func (in *Ingester) ingestNow(ctx context.Context, path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("读取 %s: %w", path, err)
	}
	mime := MIMEByExt(path)
	return in.ingestData(ctx, path, mime, data)
}

func (in *Ingester) ingestData(ctx context.Context, name, mime string, data []byte) (Document, error) {
	sha := DocumentSHA(data)
	d := Document{
		Source:    name,
		MIME:      mime,
		SHA256:    sha,
		SizeBytes: int64(len(data)),
	}

	doc, err := in.parsers.Parse(ctx, name, mime, data)
	if err != nil {
		return Document{}, err
	}
	d.Title = doc.Title
	d.CharCount = doc.CharCount()

	// 先登记文档，拿到 ID；重复内容直接返回既有记录
	id, created, err := in.repo.UpsertDocument(ctx, d)
	if err != nil {
		return Document{}, err
	}
	if !created {
		return in.repo.GetDocument(ctx, id)
	}

	chunks := in.chunker.Chunk(id, doc)
	if len(chunks) == 0 {
		_ = in.repo.SetDocumentStatus(ctx, id, DocFailed, "文档没有可索引内容")
		return in.repo.GetDocument(ctx, id)
	}

	if err := in.repo.ReplaceChunks(ctx, id, chunks); err != nil {
		_ = in.repo.SetDocumentStatus(ctx, id, DocFailed, err.Error())
		return in.repo.GetDocument(ctx, id)
	}

	_ = in.repo.SetDocumentStatus(ctx, id, DocReady, "")
	_ = in.repo.SetDocumentEmbedModel(ctx, id, "")

	in.log.Info("文档接入完成", "document", id, "source", name, "chunks", len(chunks))

	if in.queue != nil {
		_, _, err := in.queue.Enqueue(ctx, jobs.Enqueue{
			Kind:      JobEmbed,
			Payload:   embedPayload{DocumentID: id},
			DedupeKey: "doc:" + id,
		})
		if err != nil {
			in.log.Warn("入队嵌入任务失败", "document", id, "error", err)
		} else if in.runner != nil {
			in.runner.Notify()
		}
	}
	return in.repo.GetDocument(ctx, id)
}

type embedPayload struct {
	DocumentID string `json:"document_id"`
}

type rebuildPayload struct {
	Model string `json:"model"`
}

type embedHandler struct{ in *Ingester }

func (h embedHandler) Kind() string { return JobEmbed }
func (h embedHandler) Handle(ctx context.Context, job jobs.Job) error {
	var p embedPayload
	if err := job.Decode(&p); err != nil {
		return fmt.Errorf("%w: 载荷非法: %v", jobs.ErrCancel, err)
	}
	if p.DocumentID == "" {
		return fmt.Errorf("%w: 缺少 document_id", jobs.ErrCancel)
	}
	return h.in.EmbedDocument(ctx, p.DocumentID)
}

type rebuildHandler struct{ in *Ingester }

func (h rebuildHandler) Kind() string { return JobRebuild }
func (h rebuildHandler) Handle(ctx context.Context, job jobs.Job) error {
	var p rebuildPayload
	if err := job.Decode(&p); err != nil {
		return fmt.Errorf("%w: 载荷非法: %v", jobs.ErrCancel, err)
	}
	_, err := h.in.RebuildEmbeddings(ctx, p.Model)
	return err
}

// embedBatchSize 是单次 Embed 调用的条数。
//
// 对云端 API 这是省往返的关键；对 Ollama 无收益（它内部串行），
// 但也不亏——真正的成本在推理本身。
const embedBatchSize = 16

// EmbedDocument 为文档中缺少向量的片段补齐向量。
//
// 分批发送。批内有片段被后端拒绝（超长、非法字符）时，退化为逐条重试该批，
// 只跳过真正有问题的那几条——整批失败会连累无辜的片段，而逐条发送
// 又浪费了批处理的往返节省。
//
// 只处理**还没有该模型向量**的片段：重复调用不重复计费。嵌入是慢操作
// （Ollama CPU 上约 1-2 秒/条），无意义的重复调用会把「补几个新片段」
// 变成「重跑整篇」。
func (in *Ingester) EmbedDocument(ctx context.Context, documentID string) error {
	if in.embedder == nil {
		return nil // 未配置嵌入服务，只保留关键词检索
	}

	chunks, err := in.repo.ChunksByDocument(ctx, documentID)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}

	have, err := in.repo.VectorizedChunkIDs(ctx, documentID, in.model)
	if err != nil {
		return err
	}

	todo := make([]Chunk, 0, len(chunks))
	for _, c := range chunks {
		if !have[c.ID] {
			todo = append(todo, c)
		}
	}
	if len(todo) == 0 {
		// 已全部就绪：仍要确保文档上的模型标记正确
		_ = in.repo.SetDocumentEmbedModel(ctx, documentID, in.model)
		return nil
	}

	var skipped int
	var lastErr error

	for start := 0; start < len(todo); start += embedBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+embedBatchSize, len(todo))
		batch := todo[start:end]

		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.EmbedText()
		}

		vecs, _, err := in.embedder.Embed(ctx, in.model, texts)
		if err != nil {
			if !errors.Is(err, embed.ErrUnsupportedInput) {
				return err
			}
			// 整批被拒：逐条定位真凶
			for _, c := range batch {
				if err := in.embedOne(ctx, c); err != nil {
					if errors.Is(err, embed.ErrUnsupportedInput) {
						skipped++
						in.log.Warn("片段被嵌入后端拒绝，已跳过",
							"chunk", c.ID, "document", documentID, "error", err)
						continue
					}
					return err
				}
			}
			continue
		}
		if len(vecs) != len(batch) {
			return fmt.Errorf("向量数 %d 与片段数 %d 不符", len(vecs), len(batch))
		}

		for i, c := range batch {
			if err := in.storeVector(ctx, c.ID, vecs[i]); err != nil {
				lastErr = err
			}
		}
	}

	_ = in.repo.SetDocumentEmbedModel(ctx, documentID, in.model)
	in.notifyVectorsChanged()
	in.log.Info("文档向量完成",
		"document", documentID, "chunks", len(chunks),
		"embedded", len(todo), "skipped", skipped)
	return lastErr
}

// embedOne 嵌入单个片段并落库。
func (in *Ingester) embedOne(ctx context.Context, c Chunk) error {
	vecs, _, err := in.embedder.Embed(ctx, in.model, []string{c.EmbedText()})
	if err != nil {
		return err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return fmt.Errorf("片段 %s 返回的向量数量异常", c.ID)
	}
	return in.storeVector(ctx, c.ID, vecs[0])
}

func (in *Ingester) storeVector(ctx context.Context, chunkID string, v []float32) error {
	q := embed.QuantizeInt8(v)
	return in.repo.UpsertEmbedding(ctx, "chunk", chunkID, in.model, len(v), q, embed.Dot(v, v))
}

// RebuildCount 是重建的进度与结果。
// RebuildCount 是重建的结果。
type RebuildCount struct {
	Done   int
	Failed int
}

// RebuildEmbeddings 为指定模型补齐全部缺失向量。
//
// 幂等：只处理没有该模型向量的片段，跑两次第二次什么也不做。
// 换模型时旧向量保留（主键含 model），因此切回来不需要重算。
func (in *Ingester) RebuildEmbeddings(ctx context.Context, model string) (RebuildCount, error) {
	if in.embedder == nil {
		return RebuildCount{}, embed.ErrNotConfigured
	}
	if model == "" {
		model = in.model
	}

	rc := RebuildCount{}
	for {
		if err := ctx.Err(); err != nil {
			return rc, err
		}
		ids, err := in.repo.ChunkIDsWithoutVector(ctx, model, 200)
		if err != nil {
			return rc, err
		}
		if len(ids) == 0 {
			break
		}

		byID, err := in.repo.ChunksByIDs(ctx, ids)
		if err != nil {
			return rc, err
		}
		chunks := make([]Chunk, 0, len(ids))
		for _, id := range ids {
			if c, ok := byID[id]; ok {
				chunks = append(chunks, c)
			} else {
				rc.Failed++
			}
		}
		if len(chunks) == 0 {
			break
		}

		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.EmbedText()
		}
		vecs, _, err := in.embedder.Embed(ctx, model, texts)
		if err != nil {
			return rc, err
		}
		for i := range chunks {
			q := embed.QuantizeInt8(vecs[i])
			if err := in.repo.UpsertEmbedding(ctx, "chunk", chunks[i].ID, model, len(vecs[i]), q, 1); err != nil {
				rc.Failed++
				continue
			}
			rc.Done++
		}
	}

	// 文档级模型标记：只给「全部片段都有该模型向量」的文档打标。
	// 一条 SQL 完成，避免逐文档 UPDATE。
	_ = in.repo.MarkFullyVectorized(ctx, model)
	in.notifyVectorsChanged()
	return rc, nil
}

// SetEmbedder 注入嵌入后端。允许晚于构造注入，便于先建库后配服务，
// 测试里也用它模拟「换了一个嵌入模型」。
func (in *Ingester) SetEmbedder(e embed.Embedder) { in.embedder = e }

// OnVectorsChanged 注册「向量已变更」回调。
//
// bootstrap 用它把 Retriever 的缓存失效挂上：写完向量后不通知，
// 检索要等到缓存过期才看得到新文档，用户会以为 /kb add 没生效。
func (in *Ingester) OnVectorsChanged(fn func()) { in.onVectorsChanged = fn }

func (in *Ingester) notifyVectorsChanged() {
	if in.onVectorsChanged != nil {
		in.onVectorsChanged()
	}
}
