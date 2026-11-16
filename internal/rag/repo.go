package rag

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo 是 RAG 的持久化层。
type Repo struct {
	db  *sql.DB
	now func() time.Time
}

// NewRepo 构造仓储。
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db, now: time.Now}
}

// UpsertDocument 按 sha256 去重写入文档，返回文档 ID 与是否新建。
//
// 同一份内容重复上传只留一行：否则重建索引时同一段文本会被嵌入多次，
// 检索结果里出现五份一模一样的片段，用户会以为系统坏了。
func (r *Repo) UpsertDocument(ctx context.Context, d Document) (string, bool, error) {
	var existingID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE sha256 = ?`, d.SHA256).Scan(&existingID)
	if err == nil {
		return existingID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	if d.ID == "" {
		d.ID = documentID(d.SHA256, d.Source)
	}
	now := r.now().UnixMilli()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO documents (id, source, title, mime, sha256, size_bytes, char_count, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Source, d.Title, d.MIME, d.SHA256, d.SizeBytes, d.CharCount,
		string(DocIndexing), now, now)
	if err != nil {
		return "", false, fmt.Errorf("插入文档: %w", err)
	}
	return d.ID, true, nil
}

// SetDocumentStatus 更新文档状态与错误原因。
func (r *Repo) SetDocumentStatus(ctx context.Context, id string, status DocumentStatus, cause string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE documents SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		string(status), cause, r.now().UnixMilli(), id)
	return err
}

// SetDocumentEmbedModel 记录该文档当前使用的嵌入模型。
//
// 只改模型字段。早先这个函数同时写 char_count，而调用方在嵌入完成后
// 传的是 0，导致文档列表里字数变成 0——一个函数干两件事，其中一件
// 还被用错了。
func (r *Repo) SetDocumentEmbedModel(ctx context.Context, id, model string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE documents SET embed_model = ?, updated_at = ? WHERE id = ?`,
		model, r.now().UnixMilli(), id)
	return err
}

// GetDocument 按 ID 取文档。
func (r *Repo) GetDocument(ctx context.Context, id string) (Document, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, source, title, mime, sha256, size_bytes, char_count, status, error, embed_model, created_at, updated_at
		FROM documents WHERE id = ?`, id)
	return scanDocument(row)
}

// ListDocuments 列出文档，按更新时间倒序。
func (r *Repo) ListDocuments(ctx context.Context, limit int) ([]Document, error) {
	docs, _, err := r.ListDocumentsFiltered(ctx, "", 0, limit)
	return docs, err
}

// ListDocumentsFiltered 按状态与分页列出文档，返回总数。
//
// ChunkCount 用子查询填充：列表页要显示每个文档切了多少片，
// 而在 SQL 里算比逐行再查一次便宜得多。
func (r *Repo) ListDocumentsFiltered(ctx context.Context, status string, offset, limit int) ([]Document, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	where := ``
	args := []any{}
	if status != "" {
		where = ` WHERE status = ?`
		args = append(args, status)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `
		SELECT id, source, title, mime, sha256, size_bytes, char_count, status, error,
		       embed_model, created_at, updated_at,
		       (SELECT COUNT(*) FROM chunks WHERE chunks.document_id = documents.id)
		FROM documents` + where + `
		ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Document
	for rows.Next() {
		d, err := scanDocumentWithCount(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// DeleteDocument 删除文档及其切分与向量。
func (r *Repo) DeleteDocument(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 先删 FTS 里的行（contentless 表无法按内容重建，只能显式删）
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, id)
	if err != nil {
		return err
	}
	var chunkIDs []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return err
		}
		chunkIDs = append(chunkIDs, cid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, cid := range chunkIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, cid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE owner_kind = 'chunk' AND owner_id = ?`, cid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ReplaceChunks 用新切分替换文档的全部 chunk。
func (r *Repo) ReplaceChunks(ctx context.Context, documentID string, chunks []Chunk) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 删旧：先清 FTS 与向量，避免留下孤儿行
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, documentID)
	if err != nil {
		return err
	}
	var oldIDs []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return err
		}
		oldIDs = append(oldIDs, cid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, cid := range oldIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, cid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE owner_kind = 'chunk' AND owner_id = ?`, cid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, documentID); err != nil {
		return err
	}

	for _, c := range chunks {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO chunks (id, document_id, ordinal, heading_path, kind, text, char_start, char_end, page, token_est)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.DocumentID, c.Ordinal, c.HeadingPath, string(c.Kind), c.Text,
			c.CharStart, c.CharEnd, c.Page, c.TokenEst)
		if err != nil {
			return fmt.Errorf("插入片段 %s: %w", c.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chunks_fts (chunk_id, text_bigram) VALUES (?, ?)`,
			c.ID, Bigram(c.EmbedText())); err != nil {
			return fmt.Errorf("写入 FTS 片段 %s: %w", c.ID, err)
		}
	}

	return tx.Commit()
}

// ChunkCount 返回文档的片段数。
func (r *Repo) ChunkCount(ctx context.Context, documentID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE document_id = ?`, documentID).Scan(&n)
	return n, err
}

// ChunksByDocument 返回文档的全部片段，按 ordinal 排序。
func (r *Repo) ChunksByDocument(ctx context.Context, documentID string) ([]Chunk, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, document_id, ordinal, heading_path, kind, text, char_start, char_end, page, token_est
		FROM chunks WHERE document_id = ? ORDER BY ordinal`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		var c Chunk
		var kind string
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Ordinal, &c.HeadingPath, &kind,
			&c.Text, &c.CharStart, &c.CharEnd, &c.Page, &c.TokenEst); err != nil {
			return nil, err
		}
		c.Kind = SectionKind(kind)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChunkByID 按 ID 取单个片段。
func (r *Repo) ChunkByID(ctx context.Context, id string) (Chunk, error) {
	var c Chunk
	var kind string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, document_id, ordinal, heading_path, kind, text, char_start, char_end, page, token_est
		FROM chunks WHERE id = ?`, id).
		Scan(&c.ID, &c.DocumentID, &c.Ordinal, &c.HeadingPath, &kind,
			&c.Text, &c.CharStart, &c.CharEnd, &c.Page, &c.TokenEst)
	if errors.Is(err, sql.ErrNoRows) {
		return Chunk{}, ErrNotFound
	}
	c.Kind = SectionKind(kind)
	return c, err
}

// ChunksByIDs 按 ID 批量取片段。
//
// 检索路径一次要取几十条片段，逐条 ChunkByID 就是几十次往返。
// 返回 map 而不是切片：调用方通常还要按融合分重排，顺序由它自己决定。
func (r *Repo) ChunksByIDs(ctx context.Context, ids []string) (map[string]Chunk, error) {
	out := make(map[string]Chunk, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	// SQLite 的参数上限是 999，超了要分批
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		part := ids[start:end]

		placeholders := strings.Repeat("?,", len(part))
		placeholders = placeholders[:len(placeholders)-1]

		query := `SELECT id, document_id, ordinal, heading_path, kind, text, char_start, char_end, page, token_est
			FROM chunks WHERE id IN (` + placeholders + `)`
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}

		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c Chunk
			var kind string
			if err := rows.Scan(&c.ID, &c.DocumentID, &c.Ordinal, &c.HeadingPath, &kind,
				&c.Text, &c.CharStart, &c.CharEnd, &c.Page, &c.TokenEst); err != nil {
				rows.Close()
				return nil, err
			}
			c.Kind = SectionKind(kind)
			out[c.ID] = c
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TitlesByDocumentIDs 按文档 ID 批量取标题。
//
// 检索结果只需要引用到的那些文档的标题，不必把整张 documents 表读出来。
func (r *Repo) TitlesByDocumentIDs(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title FROM documents WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

// KeywordHit 是一条关键词召回结果。
type KeywordHit struct {
	ChunkID string
	Score   float32 // BM25 取负后归一化，越大越相关
}

// SearchKeyword 用 FTS5 做关键词检索。
//
// 先用严格式（全部二元组 AND），零命中再退到宽松式（OR）。
// 这不是「兜底」，而是中文二元组切分的固有问题：跨词边界会切出
// 原文不存在的组合，严格匹配必然漏掉一部分本应命中的文档。
func (r *Repo) SearchKeyword(ctx context.Context, query string, limit int) ([]KeywordHit, error) {
	strict, loose := QueryBigram(query)
	if strict == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 30
	}

	hits, err := r.searchFTS(ctx, strict, limit)
	if err != nil {
		return nil, err
	}
	if len(hits) > 0 || loose == strict {
		return hits, nil
	}
	return r.searchFTS(ctx, loose, limit)
}

func (r *Repo) searchFTS(ctx context.Context, match string, limit int) ([]KeywordHit, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT chunk_id, bm25(chunks_fts) AS rank
		FROM chunks_fts WHERE text_bigram MATCH ?
		ORDER BY rank LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("关键词检索: %w", err)
	}
	defer rows.Close()

	var out []KeywordHit
	for rows.Next() {
		var h KeywordHit
		var rank float64
		if err := rows.Scan(&h.ChunkID, &rank); err != nil {
			return nil, err
		}
		// bm25 在 SQLite 里返回负数，越小越相关；取负变成越大越相关
		h.Score = float32(-rank)
		out = append(out, h)
	}
	return out, rows.Err()
}

// UpsertEmbedding 写入量化向量。
func (r *Repo) UpsertEmbedding(ctx context.Context, ownerKind, ownerID, model string, dim int, vec []int8, norm float32) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO embeddings (owner_kind, owner_id, model, dim, vec, norm, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_kind, owner_id, model) DO UPDATE SET
			dim = excluded.dim, vec = excluded.vec, norm = excluded.norm, created_at = excluded.created_at`,
		ownerKind, ownerID, model, dim, encodeInt8(vec), norm, r.now().UnixMilli())
	return err
}

// VectorRow 是一条待扫描的向量。
type VectorRow struct {
	OwnerID string
	Vec     []int8
}

// LoadVectors 加载指定模型的全部 chunk 向量。
//
// 返回切片而不是迭代器：调用方要么整体建索引（< 15 万行），
// 要么走 StreamVectors。两档规模用两条路径，不混在一起。
func (r *Repo) LoadVectors(ctx context.Context, ownerKind, model string) ([]VectorRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT owner_id, vec FROM embeddings WHERE owner_kind = ? AND model = ?`, ownerKind, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VectorRow
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		out = append(out, VectorRow{OwnerID: id, Vec: decodeInt8(raw)})
	}
	return out, rows.Err()
}

// CountVectors 统计某模型下的向量数。
func (r *Repo) CountVectors(ctx context.Context, ownerKind, model string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM embeddings WHERE owner_kind = ? AND model = ?`, ownerKind, model).Scan(&n)
	return n, err
}

// MarkFullyVectorized 给「所有片段都已有该模型向量」的文档打上模型标记。
//
// 一条 SQL 取代「列出全部文档 + 逐个 UPDATE」：后者在 1 万篇文档时
// 就是 1 万次写，而且中间态（部分片段有向量）会被误标成完整。
func (r *Repo) MarkFullyVectorized(ctx context.Context, model string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE documents SET embed_model = ?, updated_at = ?
		WHERE embed_model <> ? AND id IN (
			SELECT c.document_id FROM chunks c
			GROUP BY c.document_id
			HAVING COUNT(*) = COUNT((
				SELECT 1 FROM embeddings e
				WHERE e.owner_kind = 'chunk' AND e.owner_id = c.id AND e.model = ?
			))
		)`, model, r.now().UnixMilli(), model, model)
	return err
}

// VectorizedChunkIDs 返回该文档中已有指定模型向量的片段 ID 集合。
func (r *Repo) VectorizedChunkIDs(ctx context.Context, documentID, model string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.owner_id FROM embeddings e
		JOIN chunks c ON c.id = e.owner_id
		WHERE e.owner_kind = 'chunk' AND e.model = ? AND c.document_id = ?`,
		model, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ChunkIDsWithoutVector 返回还没有指定模型向量的 chunk。
func (r *Repo) ChunkIDsWithoutVector(ctx context.Context, model string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id FROM chunks c
		LEFT JOIN embeddings e ON e.owner_kind = 'chunk' AND e.owner_id = c.id AND e.model = ?
		WHERE e.owner_id IS NULL
		ORDER BY c.document_id, c.ordinal
		LIMIT ?`, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteVectorsByModel 删除除 keep 之外的全部向量（换模型后的清理）。
func (r *Repo) DeleteVectorsByModel(ctx context.Context, keep string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM embeddings WHERE model <> ?`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func encodeInt8(v []int8) []byte {
	out := make([]byte, len(v))
	for i, x := range v {
		out[i] = byte(x)
	}
	return out
}

func decodeInt8(b []byte) []int8 {
	out := make([]int8, len(b))
	for i, x := range b {
		out[i] = int8(x)
	}
	return out
}

// documentID 从内容指纹生成稳定 ID。
func documentID(sha, source string) string {
	h := sha256.New()
	h.Write([]byte(sha))
	h.Write([]byte{0})
	h.Write([]byte(source))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// DocumentSHA 计算内容的指纹。
func DocumentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDocument(sc rowScanner) (Document, error) {
	var d Document
	var status string
	var created, updated int64
	err := sc.Scan(&d.ID, &d.Source, &d.Title, &d.MIME, &d.SHA256, &d.SizeBytes,
		&d.CharCount, &status, &d.Error, &d.EmbedModel, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	d.Status = DocumentStatus(status)
	d.CreatedAt = time.UnixMilli(created)
	d.UpdatedAt = time.UnixMilli(updated)
	return d, nil
}

// scanDocumentWithCount 是带片段数的版本，供列表查询使用。
func scanDocumentWithCount(sc rowScanner) (Document, error) {
	var d Document
	var status string
	var created, updated int64
	err := sc.Scan(&d.ID, &d.Source, &d.Title, &d.MIME, &d.SHA256, &d.SizeBytes,
		&d.CharCount, &status, &d.Error, &d.EmbedModel, &created, &updated, &d.ChunkCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	d.Status = DocumentStatus(status)
	d.CreatedAt = time.UnixMilli(created)
	d.UpdatedAt = time.UnixMilli(updated)
	return d, nil
}

// MIMEByExt 按扩展名猜 MIME，供没有声明类型时使用。
func MIMEByExt(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "text/html"
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".log"):
		return "text/plain"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	return ""
}
