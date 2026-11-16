// Package sqlite 实现 session.Repository。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// Repo 是 session.Repository 的 SQLite 实现。
type Repo struct {
	db *sqlite.DB
}

// New 构造仓储。
func New(db *sqlite.DB) *Repo { return &Repo{db: db} }

func (r *Repo) CreateSession(ctx context.Context, s session.Session) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions(id, title, preset_id, created_at, updated_at, archived_at, model_ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Title, s.PresetID, s.CreatedAt.Unix(), s.UpdatedAt.Unix(),
		nullableTime(s.ArchivedAt), s.ModelRef)
	return err
}

func (r *Repo) GetSession(ctx context.Context, id string) (session.Session, error) {
	var s session.Session
	var created, updated int64
	var archived sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, title, preset_id, created_at, updated_at, archived_at, model_ref
		 FROM sessions WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&s.ID, &s.Title, &s.PresetID, &created, &updated, &archived, &s.ModelRef)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Session{}, fmt.Errorf("会话 %s 不存在: %w", id, session.ErrNotFound)
		}
		return session.Session{}, err
	}
	s.CreatedAt = time.Unix(created, 0)
	s.UpdatedAt = time.Unix(updated, 0)
	s.ArchivedAt = timeFromNull(archived)
	return s, nil
}

func (r *Repo) ListSessions(ctx context.Context, limit int) ([]session.Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, preset_id, created_at, updated_at, archived_at, model_ref
		 FROM sessions WHERE deleted_at IS NULL
		 ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []session.Session
	for rows.Next() {
		var s session.Session
		var created, updated int64
		var archived sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Title, &s.PresetID, &created, &updated, &archived, &s.ModelRef); err != nil {
			return nil, err
		}
		s.CreatedAt = time.Unix(created, 0)
		s.UpdatedAt = time.Unix(updated, 0)
		s.ArchivedAt = timeFromNull(archived)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SearchSessions 按标题关键词与归档状态筛选，支持分页。
//
// 搜索放在数据库而不是前端：会话数会随使用增长，把全部会话拉到
// 浏览器再过滤既慢又费内存。
func (r *Repo) SearchSessions(ctx context.Context, opts session.SessionQuery) (session.SessionPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	var (
		where []string
		args  []any
	)
	where = append(where, "deleted_at IS NULL")
	if !opts.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		// LIKE 的通配符要转义，否则用户输入的 % 会匹配一切
		where = append(where, "title LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(q)+"%")
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE "+cond, args...).Scan(&total); err != nil {
		return session.SessionPage{}, err
	}

	pageArgs := append(append([]any(nil), args...), limit, opts.Offset)
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, preset_id, created_at, updated_at, archived_at, model_ref
		 FROM sessions WHERE `+cond+`
		 ORDER BY updated_at DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return session.SessionPage{}, err
	}
	defer rows.Close()

	page := session.SessionPage{Total: total}
	for rows.Next() {
		var s session.Session
		var created, updated int64
		var archived sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Title, &s.PresetID, &created, &updated, &archived, &s.ModelRef); err != nil {
			return session.SessionPage{}, err
		}
		s.CreatedAt = time.Unix(created, 0)
		s.UpdatedAt = time.Unix(updated, 0)
		s.ArchivedAt = timeFromNull(archived)
		page.Items = append(page.Items, s)
	}
	return page, rows.Err()
}

// escapeLike 转义 LIKE 模式里的特殊字符。
//
// SQLite 的 LIKE 默认没有转义字符，必须显式写 ESCAPE。
// 不转义的话，用户搜「100%」会匹配到所有标题。
func escapeLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// UpdateSessionModel 设置会话级模型覆盖。空串表示恢复跟随路由。
func (r *Repo) UpdateSessionModel(ctx context.Context, id, modelRef string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET model_ref = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		modelRef, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话 %s 不存在: %w", id, session.ErrNotFound)
	}
	return nil
}

func (r *Repo) UpdateSessionTitle(ctx context.Context, id, title string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
		title, time.Now().Unix(), id)
	return err
}

// ArchiveSession 归档或取消归档。传 nil 表示取消归档。
func (r *Repo) ArchiveSession(ctx context.Context, id string, at *time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET archived_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		nullableTime(at), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话 %s 不存在: %w", id, session.ErrNotFound)
	}
	return nil
}

// DeleteSession 软删除会话。
//
// 软删除而不是物理删除：消息、轮次、账本都还引用着它，
// 物理删会留下孤儿行，也让「误删能否恢复」变成不可能。
func (r *Repo) DeleteSession(ctx context.Context, id string) error {
	now := time.Now().Unix()
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("会话 %s 不存在: %w", id, session.ErrNotFound)
	}
	return nil
}

func (r *Repo) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

func (r *Repo) AppendMessage(ctx context.Context, m session.StoredMessage) error {
	content, err := session.MarshalBlocks(m.Content)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO messages(id, session_id, seq, parent_id, role, content_json, trust, pinned, compacted, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, m.Seq, nullableString(m.ParentID), string(m.Role), content,
		string(m.Trust), boolInt(m.Pinned), boolInt(m.Compacted), m.CreatedAt.Unix())
	return err
}

// GetMessage 按 ID 取单条消息。
//
// 重新生成与编辑消息需要知道目标消息的父节点，用单条查询而不是
// 先拉全表再在内存里找——长会话下后者是 O(n) 次扫描。
func (r *Repo) GetMessage(ctx context.Context, id string) (session.StoredMessage, error) {
	var m session.StoredMessage
	var parent sql.NullString
	var role, content, trust string
	var pinned, compacted int
	var created int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, session_id, seq, parent_id, role, content_json, trust, pinned, compacted, created_at
		 FROM messages WHERE id = ?`, id).
		Scan(&m.ID, &m.SessionID, &m.Seq, &parent, &role, &content,
			&trust, &pinned, &compacted, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.StoredMessage{}, fmt.Errorf("消息 %s 不存在: %w", id, session.ErrNotFound)
		}
		return session.StoredMessage{}, err
	}
	m.ParentID = parent.String
	m.Role = llm.Role(role)
	m.Trust = llm.Trust(trust)
	m.Pinned = pinned != 0
	m.Compacted = compacted != 0
	m.CreatedAt = time.Unix(created, 0)
	blocks, err := session.UnmarshalBlocks(content)
	if err != nil {
		return session.StoredMessage{}, fmt.Errorf("解析消息 %s 内容失败: %w", id, err)
	}
	m.Content = blocks
	return m, nil
}

// SearchMessages 在单个会话的消息里搜关键词。
//
// 直接在 content_json 上做 LIKE：消息内容就是这个字段的原文，
// 搜正文等于搜 JSON 里的文本节点。单会话量级在几百到几千条，
// 全表扫的代价可以接受，不值得为此再建一张全文索引表。
func (r *Repo) SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]session.StoredMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, seq, parent_id, role, content_json, trust, pinned, compacted, created_at
		 FROM messages
		 WHERE session_id = ? AND content_json LIKE ? ESCAPE '\'
		 ORDER BY seq ASC LIMIT ?`,
		sessionID, "%"+escapeLike(query)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessages 返回会话的全部消息，按 seq 升序。
func (r *Repo) ListMessages(ctx context.Context, sessionID string) ([]session.StoredMessage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, seq, parent_id, role, content_json, trust, pinned, compacted, created_at
		 FROM messages WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// scanMessages 把查询结果扫成消息列表。
//
// ListMessages 与 SearchMessages 共用：两边的列与解析规则必须一致，
// 各写一份迟早会有一边漏字段。
func scanMessages(rows *sql.Rows) ([]session.StoredMessage, error) {
	var out []session.StoredMessage
	for rows.Next() {
		var m session.StoredMessage
		var parent sql.NullString
		var role, content, trust string
		var pinned, compacted int
		var created int64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &parent, &role, &content,
			&trust, &pinned, &compacted, &created); err != nil {
			return nil, err
		}
		m.ParentID = parent.String
		m.Role = llm.Role(role)
		m.Trust = llm.Trust(trust)
		m.Pinned = pinned != 0
		m.Compacted = compacted != 0
		m.CreatedAt = time.Unix(created, 0)
		blocks, err := session.UnmarshalBlocks(content)
		if err != nil {
			return nil, fmt.Errorf("解析消息 %s 内容失败: %w", m.ID, err)
		}
		m.Content = blocks
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repo) NextSeq(ctx context.Context, sessionID string) (int, error) {
	var next sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(seq) + 1 FROM messages WHERE session_id = ?`, sessionID).Scan(&next)
	if err != nil {
		return 0, err
	}
	if !next.Valid {
		return 1, nil
	}
	return int(next.Int64), nil
}

func (r *Repo) CreateTurn(ctx context.Context, t session.Turn) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO turns(id, session_id, status, error_json, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, string(t.Status), nullableString(t.Error), t.StartedAt.Unix(), nullableTime(t.EndedAt))
	return err
}

func (r *Repo) UpdateTurnStatus(ctx context.Context, id string, status session.TurnStatus, errMsg string, endedAt *time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE turns SET status = ?, error_json = ?, ended_at = ? WHERE id = ?`,
		string(status), nullableString(errMsg), nullableTime(endedAt), id)
	return err
}

func (r *Repo) GetTurn(ctx context.Context, id string) (session.Turn, error) {
	var t session.Turn
	var status string
	var errMsg sql.NullString
	var started int64
	var ended sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, session_id, status, error_json, started_at, ended_at FROM turns WHERE id = ?`, id).
		Scan(&t.ID, &t.SessionID, &status, &errMsg, &started, &ended)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Turn{}, fmt.Errorf("轮次 %s 不存在: %w", id, session.ErrNotFound)
		}
		return session.Turn{}, err
	}
	t.Status = session.TurnStatus(status)
	t.Error = errMsg.String
	t.StartedAt = time.Unix(started, 0)
	t.EndedAt = timeFromNull(ended)
	return t, nil
}

func (r *Repo) ListTurns(ctx context.Context, sessionID string) ([]session.Turn, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, status, error_json, started_at, ended_at
		 FROM turns WHERE session_id = ? ORDER BY started_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []session.Turn
	for rows.Next() {
		var t session.Turn
		var status string
		var errMsg sql.NullString
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&t.ID, &t.SessionID, &status, &errMsg, &started, &ended); err != nil {
			return nil, err
		}
		t.Status = session.TurnStatus(status)
		t.Error = errMsg.String
		t.StartedAt = time.Unix(started, 0)
		t.EndedAt = timeFromNull(ended)
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullableTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func timeFromNull(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0)
	return &t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var _ session.Repository = (*Repo)(nil)

// FindResumable 返回最近一个可恢复的轮次（running 或 awaiting）。
func (r *Repo) FindResumable(ctx context.Context, sessionID string) (session.Turn, bool, error) {
	var t session.Turn
	var status string
	var errMsg sql.NullString
	var started int64
	var ended sql.NullInt64

	err := r.db.QueryRowContext(ctx,
		`SELECT id, session_id, status, error_json, started_at, ended_at
		 FROM turns
		 WHERE session_id = ? AND status IN ('running', 'awaiting')
		 ORDER BY started_at DESC LIMIT 1`, sessionID).
		Scan(&t.ID, &t.SessionID, &status, &errMsg, &started, &ended)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.Turn{}, false, nil
		}
		return session.Turn{}, false, err
	}
	t.Status = session.TurnStatus(status)
	t.Error = errMsg.String
	t.StartedAt = time.Unix(started, 0)
	t.EndedAt = timeFromNull(ended)
	return t, true, nil
}

// LoadSummary 读取会话的摘要状态。
func (r *Repo) LoadSummary(ctx context.Context, sessionID string) (contextx.SummaryState, bool, error) {
	var st contextx.SummaryState
	var updated int64

	err := r.db.QueryRowContext(ctx,
		`SELECT session_id, covered_through_seq, summary, extracted_facts,
		        template_version, model, tokens_in, updated_at
		 FROM summary_state WHERE session_id = ?`, sessionID).
		Scan(&st.SessionID, &st.CoveredThroughSeq, &st.Summary, &st.ExtractedFacts,
			&st.TemplateVersion, &st.Model, &st.TokensIn, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contextx.SummaryState{}, false, nil
		}
		return contextx.SummaryState{}, false, err
	}
	st.UpdatedAt = time.Unix(updated, 0)
	return st, true, nil
}

// SaveSummary 写入或更新摘要状态。
func (r *Repo) SaveSummary(ctx context.Context, st contextx.SummaryState) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO summary_state
		 (session_id, covered_through_seq, summary, extracted_facts,
		  template_version, model, tokens_in, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		   covered_through_seq = excluded.covered_through_seq,
		   summary             = excluded.summary,
		   extracted_facts     = excluded.extracted_facts,
		   template_version    = excluded.template_version,
		   model               = excluded.model,
		   tokens_in           = excluded.tokens_in,
		   updated_at          = excluded.updated_at`,
		st.SessionID, st.CoveredThroughSeq, st.Summary, st.ExtractedFacts,
		st.TemplateVersion, st.Model, st.TokensIn, st.UpdatedAt.Unix())
	return err
}

// SaveSnapshot 写入一条上下文快照。
//
// prev_snapshot_id 由 store 层自己查：这是 context_snapshots 的
// schema 知识，不该散落到 session 包的每个调用点。
func (r *Repo) SaveSnapshot(ctx context.Context, s session.ContextSnapshotInsert) error {
	segments, err := json.Marshal(s.Segments)
	if err != nil {
		return fmt.Errorf("序列化快照片段: %w", err)
	}
	budget, err := json.Marshal(s.Budget)
	if err != nil {
		return fmt.Errorf("序列化快照预算: %w", err)
	}
	totals, err := json.Marshal(map[string]int{
		"total":     s.Total,
		"available": s.Available,
	})
	if err != nil {
		return fmt.Errorf("序列化快照统计: %w", err)
	}
	bp := make([]string, len(s.CacheBreakpoints))
	for i, b := range s.CacheBreakpoints {
		bp[i] = strconv.Itoa(b)
	}

	// 同会话上一条快照。首轮取不到是正常的，返回 NULL 即可。
	var prevID sql.NullString
	_ = r.db.QueryRowContext(ctx,
		`SELECT s.id FROM context_snapshots s
		 JOIN turns t ON t.id = s.turn_id
		 WHERE t.session_id = (SELECT session_id FROM turns WHERE id = ?)
		 ORDER BY s.created_at DESC, s.step DESC LIMIT 1`, s.TurnID).Scan(&prevID)

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO context_snapshots
		 (id, turn_id, step, model, segments_json, budget_json,
		  cache_breakpoints, totals_json, prefix_hash, prev_snapshot_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TurnID, s.Step, s.Model,
		string(segments), string(budget),
		strings.Join(bp, ","), string(totals), s.PrefixHash,
		prevID, time.Now().Unix())
	return err
}

// GetContextSnapshot 取某轮次指定步的快照。step < 0 表示该轮最后一步。
func (r *Repo) GetContextSnapshot(ctx context.Context, turnID string, step int) (session.ContextSnapshot, error) {
	q := `SELECT id, turn_id, step, model, segments_json, budget_json, totals_json,
	             COALESCE(prev_snapshot_id, ''), created_at
	      FROM context_snapshots WHERE turn_id = ?`
	args := []any{turnID}
	if step >= 0 {
		q += ` AND step = ?`
		args = append(args, step)
	}
	q += ` ORDER BY step DESC LIMIT 1`

	snap, err := scanContextSnapshot(r.db.QueryRowContext(ctx, q, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.ContextSnapshot{}, session.ErrNotFound
		}
		return session.ContextSnapshot{}, err
	}
	return snap, nil
}

// GetContextSnapshotByID 按主键取快照。
func (r *Repo) GetContextSnapshotByID(ctx context.Context, id string) (session.ContextSnapshot, error) {
	snap, err := scanContextSnapshot(r.db.QueryRowContext(ctx,
		`SELECT id, turn_id, step, model, segments_json, budget_json, totals_json,
		        COALESCE(prev_snapshot_id, ''), created_at
		 FROM context_snapshots WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.ContextSnapshot{}, session.ErrNotFound
		}
		return session.ContextSnapshot{}, err
	}
	return snap, nil
}

// scanContextSnapshot 读一整行快照并解析三个 JSON 列。
func scanContextSnapshot(row *sql.Row) (session.ContextSnapshot, error) {
	var snap session.ContextSnapshot
	var segsJSON, budgetJSON, totalsJSON string
	var created int64
	if err := row.Scan(
		&snap.ID, &snap.TurnID, &snap.Step, &snap.Model,
		&segsJSON, &budgetJSON, &totalsJSON, &snap.PrevID, &created); err != nil {
		return session.ContextSnapshot{}, err
	}

	if err := json.Unmarshal([]byte(segsJSON), &snap.Segments); err != nil {
		return session.ContextSnapshot{}, fmt.Errorf("解析快照片段失败: %w", err)
	}
	if err := json.Unmarshal([]byte(budgetJSON), &snap.Budget); err != nil {
		return session.ContextSnapshot{}, fmt.Errorf("解析快照预算失败: %w", err)
	}
	var totals map[string]int
	if err := json.Unmarshal([]byte(totalsJSON), &totals); err != nil {
		return session.ContextSnapshot{}, fmt.Errorf("解析快照统计失败: %w", err)
	}
	snap.Total = totals["total"]
	snap.Available = totals["available"]
	snap.CreatedAt = time.Unix(created, 0)
	return snap, nil
}

// ListContextSnapshots 返回某会话最近的快照元信息，按时间倒序。
func (r *Repo) ListContextSnapshots(ctx context.Context, sessionID string, limit int) ([]session.ContextSnapshotMeta, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT sn.id, sn.turn_id, sn.step, sn.model, sn.totals_json, sn.created_at
		 FROM context_snapshots sn
		 JOIN turns t ON t.id = sn.turn_id
		 WHERE t.session_id = ?
		 ORDER BY sn.created_at DESC, sn.step DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]session.ContextSnapshotMeta, 0, limit)
	for rows.Next() {
		var m session.ContextSnapshotMeta
		var totalsJSON string
		var created int64
		if err := rows.Scan(&m.ID, &m.TurnID, &m.Step, &m.Model, &totalsJSON, &created); err != nil {
			return nil, err
		}
		var totals map[string]int
		if json.Unmarshal([]byte(totalsJSON), &totals) == nil {
			m.Total = totals["total"]
		}
		m.CreatedAt = time.Unix(created, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetToolResult 查询工具调用的幂等缓存。
func (r *Repo) GetToolResult(ctx context.Context, turnID, callID string) (agent.StoredToolResult, bool, error) {
	var res agent.StoredToolResult
	var isErr int
	err := r.db.QueryRowContext(ctx,
		`SELECT turn_id, tool_call_id, name, args_hash, result_json, is_error, handle, duration_ms
		 FROM tool_results WHERE turn_id = ? AND tool_call_id = ?`,
		turnID, callID).
		Scan(&res.TurnID, &res.CallID, &res.Name, &res.ArgsHash, &res.ResultJSON, &isErr, &res.Handle, &res.DurationMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agent.StoredToolResult{}, false, nil
		}
		return agent.StoredToolResult{}, false, err
	}
	res.IsError = isErr != 0
	return res, true, nil
}

// SaveToolResult 写入幂等缓存。同一键已存在时忽略。
func (r *Repo) SaveToolResult(ctx context.Context, res agent.StoredToolResult) error {
	isErr := 0
	if res.IsError {
		isErr = 1
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tool_results(turn_id, tool_call_id, name, args_hash, result_json,
		    is_error, handle, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(turn_id, tool_call_id) DO NOTHING`,
		res.TurnID, res.CallID, res.Name, res.ArgsHash, res.ResultJSON,
		isErr, res.Handle, res.DurationMS, time.Now().Unix())
	return err
}
