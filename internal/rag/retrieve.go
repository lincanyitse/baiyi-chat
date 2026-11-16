package rag

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/embed"
)

// Retriever 执行混合检索。
type Retriever struct {
	repo     *Repo
	embedder embed.Embedder
	reranker embed.Reranker
	model    string

	topKVector   int
	topKKeyword  int
	topKRerank   int
	enableRerank bool

	// staleAfter 是向量矩阵在进程内的存活时间。
	// 长时间运行的进程里新增了大量文档时，不重载会搜不到新内容；
	// 但每轮都重载等于每轮全表扫描，所以必须缓存。
	staleAfter time.Duration

	idxMu sync.Mutex
	idx   *vectorIndex
}

// RetrieverOptions 配置检索器。
type RetrieverOptions struct {
	TopKVector   int
	TopKKeyword  int
	TopKRerank   int
	EnableRerank bool
	Model        string
	StaleAfter   time.Duration
}

// NewRetriever 构造检索器。embedder 与 reranker 可为 nil（降级为关键词）。
func NewRetriever(repo *Repo, embedder embed.Embedder, reranker embed.Reranker, opts RetrieverOptions) *Retriever {
	if opts.TopKVector <= 0 {
		opts.TopKVector = 30
	}
	if opts.TopKKeyword <= 0 {
		opts.TopKKeyword = 30
	}
	if opts.TopKRerank <= 0 {
		opts.TopKRerank = 10
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 10 * time.Minute
	}
	return &Retriever{
		repo:         repo,
		embedder:     embedder,
		reranker:     reranker,
		model:        opts.Model,
		topKVector:   opts.TopKVector,
		topKKeyword:  opts.TopKKeyword,
		topKRerank:   opts.TopKRerank,
		enableRerank: opts.EnableRerank,
		staleAfter:   opts.StaleAfter,
	}
}

// Search 执行一次混合检索。
//
// 任何一路失败都不会让整体失败：向量不可用就只用关键词，
// 重排不可用就按 RRF 分数排序——用户问问题时得到「稍微差一点的答案」
// 远好于得到一个错误提示。
func (r *Retriever) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{Chunks: []Citation{}}, nil
	}
	if limit <= 0 {
		limit = r.topKRerank
	}

	var res SearchResult
	var vectorIDs []string
	// vecScores 记录真实的余弦相似度。RRF 分只用于排序，
	// 量级是 1/(60+rank)，展示给用户会被误读成「相关性 3%」。
	vecScores := map[string]float32{}

	if r.embedder != nil {
		hits, err := r.vectorCandidates(ctx, query)
		if err != nil {
			if !errors.Is(err, embed.ErrNotConfigured) && !errors.Is(err, embed.ErrUnavailable) {
				return SearchResult{}, fmt.Errorf("向量检索: %w", err)
			}
			res.Degraded = true
			res.Reason = "向量服务不可用，已降级为关键词检索"
		} else {
			for _, h := range hits {
				vectorIDs = append(vectorIDs, h.ID)
				vecScores[h.ID] = h.Score
			}
		}
	} else {
		res.Degraded = true
		res.Reason = "未配置嵌入服务，仅使用关键词检索"
	}

	keywordHits, err := r.repo.SearchKeyword(ctx, query, r.topKKeyword)
	if err != nil {
		// 关键词检索是最后的兜底，它挂了才真的没结果可用
		if len(vectorIDs) == 0 {
			return SearchResult{}, fmt.Errorf("关键词检索: %w", err)
		}
		res.Reason = joinReason(res.Reason, "关键词检索失败")
	}

	keywordIDs := make([]string, len(keywordHits))
	for i, h := range keywordHits {
		keywordIDs[i] = h.ChunkID
	}

	fused := topN(RRF(vectorIDs, keywordIDs), max(limit*5, 50))

	// 全部召回为空：不是错误，只是没找到
	if len(fused) == 0 {
		return res, nil
	}

	chunks, err := r.loadChunks(ctx, fused, vecScores)
	if err != nil {
		return SearchResult{}, err
	}
	if len(chunks) == 0 {
		return res, nil
	}

	// 重排
	if r.enableRerank && r.reranker != nil {
		reranked, ok := r.rerank(ctx, query, chunks)
		if ok {
			chunks = reranked
			res.Reranked = true
		} else {
			res.Reason = joinReason(res.Reason, "重排服务不可用，已按融合分排序")
		}
	}

	if len(chunks) > limit {
		chunks = chunks[:limit]
	}
	res.Chunks = chunks
	return res, nil
}

// vectorCandidates 返回向量的 top-N 片段及其余弦相似度。
func (r *Retriever) vectorCandidates(ctx context.Context, query string) ([]embed.Hit, error) {
	if r.model == "" {
		return nil, embed.ErrNotConfigured
	}

	vecs, _, err := r.embedder.Embed(ctx, r.model, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("查询向量为空")
	}

	idx, err := r.vectorIndex(ctx)
	if err != nil {
		return nil, err
	}
	if idx == nil || len(idx.ids) == 0 {
		return nil, nil
	}

	return embed.TopK(embed.QuantizeInt8(vecs[0]), idx.ids, idx.vecs, r.topKVector), nil
}

// vectorIndex 是进程内的向量矩阵。
type vectorIndex struct {
	ids      []string
	vecs     [][]int8
	loadedAt time.Time
}

// vectorIndex 返回缓存的向量矩阵，超过 staleAfter 或未加载时重新读取。
//
// 每次检索都从 SQLite 全量读 BLOB 再反量化，10 万片段就是 100MB 的
// 解码开销——而它们几乎不变。缓存后每轮只剩一次点积扫描。
//
// 失效策略是「时间」而非「写入通知」：ingest 与检索可能在不同 goroutine，
// 通知机制要引入锁与回调；个人规模下 10 分钟的延迟完全可接受，
// 且 RebuildEmbeddings 结束时可以显式 Invalidate。
func (r *Retriever) vectorIndex(ctx context.Context) (*vectorIndex, error) {
	r.idxMu.Lock()
	defer r.idxMu.Unlock()

	if r.idx != nil && time.Since(r.idx.loadedAt) < r.staleAfter {
		return r.idx, nil
	}

	rows, err := r.repo.LoadVectors(ctx, "chunk", r.model)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		r.idx = &vectorIndex{loadedAt: time.Now()}
		return r.idx, nil
	}

	idx := &vectorIndex{
		ids:      make([]string, len(rows)),
		vecs:     make([][]int8, len(rows)),
		loadedAt: time.Now(),
	}
	for i, row := range rows {
		idx.ids[i] = row.OwnerID
		idx.vecs[i] = row.Vec
	}
	r.idx = idx
	return idx, nil
}

// Invalidate 丢弃向量缓存。写入新向量后调用，让下一次检索立即看到它们。
func (r *Retriever) Invalidate() {
	r.idxMu.Lock()
	r.idx = nil
	r.idxMu.Unlock()
}

// loadChunks 按 ID 批量载入片段，保持 fused 的顺序。
func (r *Retriever) loadChunks(ctx context.Context, fused []Hit, vecScores map[string]float32) ([]Citation, error) {
	ids := make([]string, len(fused))
	for i, h := range fused {
		ids[i] = h.ID
	}

	chunks, err := r.repo.ChunksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	// 只查引用到的文档标题，不读整张表
	docIDs := make([]string, 0, len(chunks))
	seen := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		if !seen[c.DocumentID] {
			seen[c.DocumentID] = true
			docIDs = append(docIDs, c.DocumentID)
		}
	}
	titles, err := r.repo.TitlesByDocumentIDs(ctx, docIDs)
	if err != nil {
		return nil, err
	}

	scoreByID := make(map[string]float32, len(fused))
	for _, h := range fused {
		scoreByID[h.ID] = h.Score
	}

	out := make([]Citation, 0, len(fused))
	for _, h := range fused {
		ch, ok := chunks[h.ID]
		if !ok {
			continue // 索引与存储不一致时跳过而不是整体失败
		}
		out = append(out, Citation{
			ChunkID:     ch.ID,
			DocumentID:  ch.DocumentID,
			Title:       titles[ch.DocumentID],
			HeadingPath: ch.HeadingPath,
			Page:        ch.Page,
			CharStart:   ch.CharStart,
			CharEnd:     ch.CharEnd,
			Snippet:     ch.Text,
			Score:       scoreByID[h.ID],
			VectorScore: vectorScoreOf(vecScores, ch.ID),
		})
	}
	return out, nil
}

// vectorScoreOf 取余弦相似度；未命中向量路时返回 -1（表示未知，不是「不相关」）。
func vectorScoreOf(scores map[string]float32, id string) float32 {
	if s, ok := scores[id]; ok {
		return s
	}
	return -1
}

// rerank 用外部模型重排。失败时返回 false，调用方沿用 RRF 分。
func (r *Retriever) rerank(ctx context.Context, query string, cits []Citation) ([]Citation, bool) {
	docs := make([]string, len(cits))
	for i, c := range cits {
		docs[i] = c.Snippet
	}

	scores, _, err := r.reranker.Rerank(ctx, "", query, docs)
	if err != nil {
		return nil, false
	}
	if len(scores) != len(cits) {
		return nil, false
	}

	out := make([]Citation, len(cits))
	copy(out, cits)
	for i := range out {
		out[i].Score = scores[i]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, true
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}
