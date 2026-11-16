package rag

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/jobs"
)

func newTestRepo(t *testing.T) (*Repo, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewRepo(db.DB), db.DB
}

func ingest(t *testing.T, r *Repo, name, content string) Document {
	t.Helper()
	ctx := context.Background()
	parsers := NewParserRegistry()
	doc, err := parsers.Parse(ctx, name, MIMEByExt(name), []byte(content))
	if err != nil {
		t.Fatal(err)
	}

	id, created, err := r.UpsertDocument(ctx, Document{
		Source: name, Title: doc.Title, MIME: doc.MIME,
		SHA256: DocumentSHA([]byte(content)), SizeBytes: int64(len(content)),
		CharCount: doc.CharCount(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		chunks := NewChunker(300, 0.1).Chunk(id, doc)
		if err := r.ReplaceChunks(ctx, id, chunks); err != nil {
			t.Fatal(err)
		}
		if err := r.SetDocumentStatus(ctx, id, DocReady, ""); err != nil {
			t.Fatal(err)
		}
	}
	d, err := r.GetDocument(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUpsertDocumentDeduplicates(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	content := "# 手册\n\n白泽是一个个人助理。"
	a := ingest(t, r, "a.md", content)
	b := ingest(t, r, "b.md", content) // 同内容不同文件名

	if a.ID != b.ID {
		t.Fatalf("同内容应复用同一文档：%s vs %s", a.ID, b.ID)
	}

	docs, err := r.ListDocuments(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("应只有 1 份文档，实际 %d", len(docs))
	}
}

func TestChunkCountAndFetch(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n第一段。\n\n第二段。\n")
	n, err := r.ChunkCount(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("短文档应只有 1 块，实际 %d", n)
	}

	chunks, err := r.ChunksByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ChunkByID(ctx, chunks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != chunks[0].Text {
		t.Fatalf("按 ID 取回的内容不一致：%q vs %q", got.Text, chunks[0].Text)
	}
}

func TestSearchKeyword(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	ingest(t, r, "a.md", "# 部署\n\n容器部署需要先拉取镜像。\n")
	ingest(t, r, "b.md", "# 记忆\n\n白泽可以跨会话记住偏好。\n")

	hits, err := r.SearchKeyword(ctx, "容器部署", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("应命中部署文档")
	}

	ch, err := r.ChunkByID(ctx, hits[0].ChunkID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Text, "容器") {
		t.Fatalf("命中了错误的片段：%q", ch.Text)
	}
}

func TestSearchKeywordNoMatch(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	ingest(t, r, "a.md", "# 甲\n\n内容。\n")

	hits, err := r.SearchKeyword(ctx, "完全无关的词汇组合", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("不该有命中，实际 %d", len(hits))
	}
}

func TestDeleteDocumentCleansAll(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n正文。\n")
	chunks, _ := r.ChunksByDocument(ctx, doc.ID)
	if err := r.UpsertEmbedding(ctx, "chunk", chunks[0].ID, "m", 4, []int8{1, 2, 3, 4}, 1); err != nil {
		t.Fatal(err)
	}

	if err := r.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.GetDocument(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("文档应已删除，实际 %v", err)
	}
	if n, _ := r.ChunkCount(ctx, doc.ID); n != 0 {
		t.Fatalf("片段应已删除，实际 %d", n)
	}
	if n, _ := r.CountVectors(ctx, "chunk", "m"); n != 0 {
		t.Fatalf("向量应已删除，实际 %d", n)
	}
	hits, _ := r.SearchKeyword(ctx, "甲 正文", 10)
	if len(hits) != 0 {
		t.Fatalf("FTS 应已清理，实际 %d 条", len(hits))
	}
}

func TestReplaceChunksIsIdempotent(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n正文。\n")
	before, _ := r.ChunksByDocument(ctx, doc.ID)

	if err := r.ReplaceChunks(ctx, doc.ID, before); err != nil {
		t.Fatal(err)
	}
	after, _ := r.ChunksByDocument(ctx, doc.ID)

	if len(after) != len(before) {
		t.Fatalf("重复替换不该改变数量：%d → %d", len(before), len(after))
	}
	if after[0].ID != before[0].ID {
		t.Fatal("同样内容应生成同样 ID")
	}
	// FTS 不该产生重复行导致重复命中
	hits, _ := r.SearchKeyword(ctx, "正文", 10)
	if len(hits) != 1 {
		t.Fatalf("重复替换后 FTS 应无重复，实际 %d 条", len(hits))
	}
}

func TestUpsertEmbeddingOverwrites(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	if err := r.UpsertEmbedding(ctx, "chunk", "c1", "m", 2, []int8{1, 2}, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertEmbedding(ctx, "chunk", "c1", "m", 2, []int8{3, 4}, 1); err != nil {
		t.Fatal(err)
	}

	rows, err := r.LoadVectors(ctx, "chunk", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("同主键应覆盖而非新增，实际 %d 行", len(rows))
	}
	if rows[0].Vec[0] != 3 {
		t.Fatalf("应写入新值：%v", rows[0].Vec)
	}
}

func TestVectorsCoexistByModel(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	if err := r.UpsertEmbedding(ctx, "chunk", "c1", "m1", 2, []int8{1, 2}, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertEmbedding(ctx, "chunk", "c1", "m2", 2, []int8{3, 4}, 1); err != nil {
		t.Fatal(err)
	}

	n1, _ := r.CountVectors(ctx, "chunk", "m1")
	n2, _ := r.CountVectors(ctx, "chunk", "m2")
	if n1 != 1 || n2 != 1 {
		t.Fatalf("不同模型应共存：m1=%d m2=%d", n1, n2)
	}
}

func TestChunkIDsWithoutVector(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n第一段。\n\n第二段。\n")
	chunks, _ := r.ChunksByDocument(ctx, doc.ID)

	ids, err := r.ChunkIDsWithoutVector(ctx, "m", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(chunks) {
		t.Fatalf("全部片段都缺向量，实际返回 %d / %d", len(ids), len(chunks))
	}

	if err := r.UpsertEmbedding(ctx, "chunk", chunks[0].ID, "m", 4, []int8{1, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	ids, _ = r.ChunkIDsWithoutVector(ctx, "m", 100)
	if len(ids) != len(chunks)-1 {
		t.Fatalf("应只剩 %d 条，实际 %d", len(chunks)-1, len(ids))
	}
}

func TestDeleteVectorsByModel(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	r.UpsertEmbedding(ctx, "chunk", "c1", "old", 2, []int8{1, 2}, 1)
	r.UpsertEmbedding(ctx, "chunk", "c2", "new", 2, []int8{1, 2}, 1)

	n, err := r.DeleteVectorsByModel(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应删 1 行，实际 %d", n)
	}
	if c, _ := r.CountVectors(ctx, "chunk", "new"); c != 1 {
		t.Fatal("保留的模型不该被删")
	}
}

// 端到端：检索 + 引用。
func TestSearchEndToEndWithFakeEmbedder(t *testing.T) {
	r, db := newTestRepo(t)
	ctx := context.Background()

	content := `# 白泽手册

## 概述

白泽是一个个人 AI 助手，可以跨会话记住你说过的偏好。

## 部署

部署需要先拉取 Ollama 镜像，然后启动容器并拉取 bge-m3 模型。
`

	doc := ingest(t, r, "manual.md", content)

	// 用 fake 嵌入补齐向量
	f := embed.NewFake(256)
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake", Log: nopLogger{}})
	ing.SetEmbedder(f)
	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	if n, _ := r.CountVectors(ctx, "chunk", "fake"); n == 0 {
		t.Fatal("应写入向量")
	}

	ret := NewRetriever(r, f, nil, RetrieverOptions{Model: "fake", EnableRerank: false})
	res, err := ret.Search(ctx, "部署 Ollama", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("应有检索结果")
	}
	if res.Degraded {
		t.Fatalf("配了嵌入服务不该降级：%s", res.Reason)
	}

	// 引用应带可定位信息
	c := res.Chunks[0]
	if c.ChunkID == "" || c.DocumentID != doc.ID {
		t.Fatalf("引用缺少定位信息：%+v", c)
	}
	if c.HeadingPath == "" {
		t.Fatal("引用应带 heading_path")
	}

	// 渲染成模型可读文本
	block := CitationBlock(res)
	if !strings.Contains(block, "[1]") {
		t.Fatalf("应带编号引用：%q", block)
	}
	if !strings.Contains(block, "引用时请标注") {
		t.Fatalf("应提示模型标注引用：%q", block)
	}

	// CitationBlock 应能转成 llm 块
	blocks := CitationBlocks(res)
	if len(blocks) != len(res.Chunks) {
		t.Fatalf("引用块数不符：%d vs %d", len(blocks), len(res.Chunks))
	}

	_ = db
}

func TestSearchDegradesWhenEmbedderUnavailable(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	ingest(t, r, "a.md", "# 部署\n\n容器部署需要拉取镜像。\n")

	f := embed.NewFake(64)
	f.Unavailable = true
	ret := NewRetriever(r, f, nil, RetrieverOptions{Model: "fake"})

	res, err := ret.Search(ctx, "容器部署", 5)
	if err != nil {
		t.Fatalf("嵌入不可用不该让检索失败：%v", err)
	}
	if !res.Degraded {
		t.Fatal("应标记为降级")
	}
	if len(res.Chunks) == 0 {
		t.Fatal("降级后关键词仍应召回")
	}
}

func TestSearchWithoutEmbedder(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	ingest(t, r, "a.md", "# 甲\n\n白泽助理。\n")

	ret := NewRetriever(r, nil, nil, RetrieverOptions{})
	res, err := ret.Search(ctx, "白泽", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded {
		t.Fatal("未配置嵌入应标记降级")
	}
	if len(res.Chunks) == 0 {
		t.Fatal("关键词应召回")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	r, _ := newTestRepo(t)
	ret := NewRetriever(r, nil, nil, RetrieverOptions{})

	res, err := ret.Search(context.Background(), "   ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 0 {
		t.Fatal("空查询应返回空结果")
	}
}

func TestSearchLimit(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	var b strings.Builder
	b.WriteString("# 甲\n\n")
	for i := range 30 {
		b.WriteString("白泽助手条目")
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString("，内容各不相同用于分开切块。\n\n")
	}
	ingest(t, r, "a.md", b.String())

	ret := NewRetriever(r, nil, nil, RetrieverOptions{})
	res, err := ret.Search(ctx, "白泽助手", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) > 3 {
		t.Fatalf("应限制在 3 条，实际 %d", len(res.Chunks))
	}
}

func TestRetrieveWithReranker(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	ingest(t, r, "a.md", "# 甲\n\n白泽是个助理。\n")
	ingest(t, r, "b.md", "# 乙\n\n天气预报说明天下雨。\n")

	ret := NewRetriever(r, nil, &embed.FakeReranker{}, RetrieverOptions{EnableRerank: true})
	res, err := ret.Search(ctx, "白泽助理", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reranked {
		t.Fatal("应经过重排")
	}
	if len(res.Chunks) == 0 || !strings.Contains(res.Chunks[0].Snippet, "白泽") {
		t.Fatalf("重排后应把相关文档排前面：%+v", res.Chunks)
	}
}

func TestRetrieveRerankerFailureFallsBack(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	ingest(t, r, "a.md", "# 甲\n\n白泽助理。\n")

	rr := &embed.FakeReranker{Unavailable: true}
	ret := NewRetriever(r, nil, rr, RetrieverOptions{EnableRerank: true})
	res, err := ret.Search(ctx, "白泽", 5)
	if err != nil {
		t.Fatalf("重排失败不该让检索失败：%v", err)
	}
	if res.Reranked {
		t.Fatal("失败时不该标记已重排")
	}
	if len(res.Chunks) == 0 {
		t.Fatal("仍应返回 RRF 结果")
	}
	if !strings.Contains(res.Reason, "重排") {
		t.Fatalf("应说明重排不可用：%q", res.Reason)
	}
}

func TestIngestFileSync(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	dir := t.TempDir()
	path := dir + "/note.md"
	if err := writeFile(path, "# 笔记\n\n这是本地文件的正文。\n"); err != nil {
		t.Fatal(err)
	}

	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	doc, err := ing.IngestFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != DocReady {
		t.Fatalf("应就绪，实际 %s（%s）", doc.Status, doc.Error)
	}
	if doc.CharCount == 0 {
		t.Fatal("应记录字符数")
	}
}

func TestIngestRejectsNonUTF8(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	_, err := ing.IngestBytes(ctx, "gbk.txt", "text/plain", []byte{0xC4, 0xE3, 0xBA, 0xC3})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("非 UTF-8 应报错，实际 %v", err)
	}
}

func TestRebuildEmbeddingsIsIdempotent(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	ingest(t, r, "a.md", "# 甲\n\n正文一句话。\n")

	counting := &countingEmbedder{inner: embed.NewFake(64)}
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	ing.SetEmbedder(counting)

	first, err := ing.RebuildEmbeddings(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if first.Done == 0 {
		t.Fatal("首次重建应有处理量")
	}
	callsAfterFirst := counting.calls
	if callsAfterFirst == 0 {
		t.Fatal("首次应调用嵌入")
	}

	second, err := ing.RebuildEmbeddings(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if second.Done != 0 {
		t.Fatalf("第二次应无处理量，实际 %d", second.Done)
	}
	if counting.calls != callsAfterFirst {
		t.Fatalf("第二次不该再调用嵌入：%d → %d", callsAfterFirst, counting.calls)
	}
}

func TestRebuildKeepsOtherModels(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n正文。\n")
	chunks, _ := r.ChunksByDocument(ctx, doc.ID)
	_ = r.UpsertEmbedding(ctx, "chunk", chunks[0].ID, "old", 4, []int8{1, 0, 0, 0}, 1)

	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "new"})
	ing.SetEmbedder(embed.NewFake(64))
	if _, err := ing.RebuildEmbeddings(ctx, "new"); err != nil {
		t.Fatal(err)
	}

	oldN, _ := r.CountVectors(ctx, "chunk", "old")
	if oldN != 1 {
		t.Fatalf("换模型不该删掉旧向量，实际 %d", oldN)
	}
}

func TestIngestQueuesEmbedJob(t *testing.T) {
	r, db := newTestRepo(t)
	ctx := context.Background()

	dir := t.TempDir()
	path := dir + "/a.md"
	writeFile(path, "# 甲\n\n正文。\n")

	store := newQueue(t, db)
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{
		Model: "fake", Queue: store,
	})
	if _, err := ing.IngestFile(ctx, path); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.ListRecent(ctx, JobEmbed, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("应入队 1 个嵌入任务，实际 %d", len(jobs))
	}
}

type countingEmbedder struct {
	inner embed.Embedder
	calls int
}

func (c *countingEmbedder) Name() string            { return c.inner.Name() }
func (c *countingEmbedder) Dimensions(m string) int { return c.inner.Dimensions(m) }
func (c *countingEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, embed.Usage, error) {
	c.calls++
	return c.inner.Embed(ctx, model, texts)
}

func TestEmbedDocumentNoEmbedderIsSilent(t *testing.T) {
	r, _ := newTestRepo(t)
	doc := ingest(t, r, "a.md", "# 甲\n\n正文。\n")

	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{})
	if err := ing.EmbedDocument(context.Background(), doc.ID); err != nil {
		t.Fatalf("未配嵌入时应静默返回，实际 %v", err)
	}
}

func TestDocumentTimestamps(t *testing.T) {
	r, _ := newTestRepo(t)
	doc := ingest(t, r, "a.md", "# 甲\n\n正文。\n")
	if doc.CreatedAt.IsZero() || doc.UpdatedAt.IsZero() {
		t.Fatal("应有时间戳")
	}
	if time.Since(doc.CreatedAt) > time.Minute {
		t.Fatalf("时间戳异常：%v", doc.CreatedAt)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func newQueue(t *testing.T, db *sql.DB) *jobs.Store {
	t.Helper()
	return jobs.NewStore(db)
}

// TestEmbedModelUpdateKeepsCharCount 锁定一个真实缺陷：
// 早先 SetDocumentEmbedModel 同时写 char_count，调用方传 0，
// 导致嵌入完成后文档列表里字数显示为 0。
func TestEmbedModelUpdateKeepsCharCount(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n这是一段有内容的正文。\n")
	if doc.CharCount == 0 {
		t.Fatal("前置条件：接入后应有字符数")
	}

	if err := r.SetDocumentEmbedModel(ctx, doc.ID, "bge-m3"); err != nil {
		t.Fatal(err)
	}

	after, err := r.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.EmbedModel != "bge-m3" {
		t.Fatalf("模型未更新：%q", after.EmbedModel)
	}
	if after.CharCount != doc.CharCount {
		t.Fatalf("字符数被误改：%d → %d", doc.CharCount, after.CharCount)
	}
}

// TestEmbedDocumentSkipsExistingVectors 锁定一个真实缺陷：
// EmbedDocument 曾无条件重算全部片段，导致重复调用重复计费。
// Ollama CPU 上每条 1-2 秒，重跑一篇 100 片段的文档要两分钟。
func TestEmbedDocumentSkipsExistingVectors(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n第一段正文。\n\n第二段正文。\n")

	c := &countingEmbedder{inner: embed.NewFake(64)}
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	ing.SetEmbedder(c)

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	first := c.calls
	if first == 0 {
		t.Fatal("首次应调用嵌入")
	}

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	if c.calls != first {
		t.Fatalf("已全部有向量时不该再调用：%d → %d", first, c.calls)
	}
}

// TestEmbedDocumentOnlyFillsGaps 验证新增片段后只补新片段。
func TestEmbedDocumentOnlyFillsGaps(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n原有正文内容。\n")
	c := &countingEmbedder{inner: embed.NewFake(64)}
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	ing.SetEmbedder(c)

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := r.CountVectors(ctx, "chunk", "fake")
	callsBefore := c.calls

	// 手工塞一个新片段（模拟文档更新后新增内容）
	chunks, _ := r.ChunksByDocument(ctx, doc.ID)
	newChunk := chunks[0]
	newChunk.ID = "brand-new-chunk"
	newChunk.Text = "更新后新增的正文"
	if err := r.ReplaceChunks(ctx, doc.ID, append(chunks, newChunk)); err != nil {
		t.Fatal(err)
	}

	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	after, _ := r.CountVectors(ctx, "chunk", "fake")
	if after != before+1 {
		t.Fatalf("应只补 1 条新向量：%d → %d", before, after)
	}
	if c.calls != callsBefore+1 {
		t.Fatalf("应只调用 1 次嵌入：%d → %d", callsBefore, c.calls)
	}
}

// TestVectorIndexCacheInvalidation 验证检索缓存的两个不变量：
// 1) 同一份数据连续检索不重复读表（缓存命中）
// 2) Invalidate 之后立刻能看到新写入的向量
//
// 前者是性能（每次检索全表扫描 10 万行不可接受），
// 后者是正确性（/kb add 之后用户马上搜，必须搜得到）。
func TestVectorIndexCacheInvalidation(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	doc := ingest(t, r, "a.md", "# 甲\n\n第一个文档的正文内容。\n")
	f := embed.NewFake(64)
	ing := NewIngester(r, NewParserRegistry(), IngesterOptions{Model: "fake"})
	ing.SetEmbedder(f)
	if err := ing.EmbedDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	ret := NewRetriever(r, f, nil, RetrieverOptions{Model: "fake", EnableRerank: false})

	// 第一次检索：建立缓存
	res, err := ret.Search(ctx, "第一个文档", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("应有结果")
	}

	// 缓存已建立
	if ret.idx == nil {
		t.Fatal("首次检索后应建立向量缓存")
	}
	firstIdx := ret.idx

	// 第二次检索：应复用同一份矩阵（指针相同即未重载）
	if _, err := ret.Search(ctx, "第一个文档", 5); err != nil {
		t.Fatal(err)
	}
	if ret.idx != firstIdx {
		t.Fatal("第二次检索不该重新读表")
	}

	// 新加一个文档并嵌入，然后 Invalidate
	doc2 := ingest(t, r, "b.md", "# 乙\n\n第二个文档讲的是完全不同的主题。\n")
	if err := ing.EmbedDocument(ctx, doc2.ID); err != nil {
		t.Fatal(err)
	}
	// Ingester 已通过回调自动失效（OnVectorsChanged 未注册时靠手动）
	ret.Invalidate()

	if _, err := ret.Search(ctx, "第二个文档", 5); err != nil {
		t.Fatal(err)
	}
	// 用指针比较而不是时间戳：两次加载可能落在同一毫秒内
	if ret.idx == nil || ret.idx == firstIdx {
		t.Fatal("Invalidate 之后应重新读表")
	}
	if len(ret.idx.ids) != 2 {
		t.Fatalf("重新读表后应包含两个文档的向量，实际 %d 条", len(ret.idx.ids))
	}
}
