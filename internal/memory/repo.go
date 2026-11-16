package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo 是记忆的持久化层。
type Repo struct {
	db  *sql.DB
	now func() time.Time
}

// NewRepo 构造仓储。
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db, now: time.Now} }

// InsertCandidate 记录一条候选并返回 ID。
//
// 候选先落库再判定，是为了留下「为什么这条没进记忆」的证据——
// 用户怀疑漏记时能查到原因，而不是面对一个黑箱。
func (r *Repo) InsertCandidate(ctx context.Context, c Candidate) (string, error) {
	if c.Subject == "" || c.Predicate == "" || c.Value == "" {
		return "", ErrInvalidCandidate
	}
	if c.SourceType == "" {
		c.SourceType = SourceTurn
	}
	if c.Confidence <= 0 {
		c.Confidence = 0.5
	}
	if c.ID == "" {
		c.ID = newID("cand")
	}
	now := r.now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO memory_candidates
			(id, session_id, turn_id, subject, predicate, value, source_type, confidence, resolution, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, c.TurnID, c.Subject, c.Predicate, c.Value,
		string(c.SourceType), c.Confidence, ResPending, now)
	if err != nil {
		return "", fmt.Errorf("插入候选: %w", err)
	}
	return c.ID, nil
}

// ResolveCandidate 记录候选的最终归属。
func (r *Repo) ResolveCandidate(ctx context.Context, id, resolution, factID, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE memory_candidates SET resolution = ?, resolved_id = ?, reason = ? WHERE id = ?`,
		resolution, factID, reason, id)
	return err
}

// ActiveFact 按归一化键查当前生效的事实。
func (r *Repo) ActiveFact(ctx context.Context, subjectKey, predicateKey string) (Fact, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+factColumns+` FROM facts
		WHERE subject_key = ? AND predicate_key = ? AND status = 'active' AND deleted_at IS NULL`,
		subjectKey, predicateKey)
	return scanFact(row)
}

// FactsBySubject 按主体取出全部生效事实，供画像卡渲染。
func (r *Repo) FactsBySubject(ctx context.Context, subjectKey string) ([]Fact, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+factColumns+` FROM facts
		WHERE subject_key = ? AND status = 'active' AND deleted_at IS NULL
		ORDER BY importance DESC, confirmations DESC, updated_at DESC`, subjectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

// AllActiveFacts 取出全部生效事实，按可信度与重要性排序。
//
// 硬过滤 source_type：外部内容即便被误写成 facts 行，也不会进画像卡。
func (r *Repo) AllActiveFacts(ctx context.Context, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+factColumns+` FROM facts
		WHERE status = 'active' AND deleted_at IS NULL
		  AND source_type IN ('explicit', 'turn', 'session_end', 'imported')
		ORDER BY importance DESC, confirmations DESC, updated_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

// InsertFact 插入一条新事实。
//
// status 由调用方指定：需要取代旧行时用 SupersedeAndInsert，
// 它会先让旧行失效再插入——唯一索引只允许一条 active，顺序反了会撞约束。
func (r *Repo) InsertFact(ctx context.Context, f Fact) (string, error) {
	f = withFactDefaults(f, r.now())
	if _, err := r.db.ExecContext(ctx, insertFactSQL, insertFactArgs(f)...); err != nil {
		return "", fmt.Errorf("插入事实: %w", err)
	}
	return f.ID, nil
}

// GetFact 按 ID 取一条事实（含已失效的历史版本）。
//
// 管理界面编辑要走它：编辑需要旧行的完整字段，用来继承主体、
// 谓词与重要性。
func (r *Repo) GetFact(ctx context.Context, id string) (Fact, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+factColumns+` FROM facts WHERE id = ?`, id)

	f, err := scanFact(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Fact{}, ErrNotFound
		}
		return Fact{}, err
	}
	return f, nil
}

// SupersedeAndInsert 在一个事务里完成「旧行失效 + 新行生效」。
//
// 必须原子：中间失败会留下「没有 active 行」的状态，
// 那条记忆就凭空消失了。唯一索引也要求旧行先让位。
func (r *Repo) SupersedeAndInsert(ctx context.Context, oldID string, f Fact) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	now := r.now()
	f = withFactDefaults(f, now)
	f.Status = StatusActive

	if _, err := tx.ExecContext(ctx, `
		UPDATE facts SET status = 'superseded', valid_to = ?, superseded_by = ?, updated_at = ?
		WHERE id = ?`, now.UnixMilli(), f.ID, now.UnixMilli(), oldID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, insertFactSQL, insertFactArgs(f)...); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return f.ID, nil
}

// withFactDefaults 补齐可选字段的零值。
//
// 两个插入路径共用，避免默认值在两处慢慢分叉。
func withFactDefaults(f Fact, now time.Time) Fact {
	if f.ID == "" {
		f.ID = newID("fact")
	}
	if f.Status == "" {
		f.Status = StatusActive
	}
	if f.Status == StatusActive {
		f.ValidTo = nil // active 行不该有终止时间
	}
	if f.ValidFrom.IsZero() {
		f.ValidFrom = now
	}
	if f.Confidence <= 0 {
		f.Confidence = 0.6
	}
	if f.Importance <= 0 {
		f.Importance = 0.5
	}
	if f.Confirmations <= 0 {
		f.Confirmations = 1
	}
	if f.SourceType == "" {
		f.SourceType = SourceTurn
	}
	return f
}

const insertFactSQL = `
	INSERT INTO facts
		(id, subject, subject_key, predicate, predicate_key, value, value_key,
		 status, confidence, importance, confirmations, access_count,
		 source_type, source_ref, session_id, turn_id,
		 valid_from, valid_to, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertFactArgs(f Fact) []any {
	var validTo any
	if f.ValidTo != nil {
		validTo = f.ValidTo.UnixMilli()
	}
	now := f.ValidFrom.UnixMilli()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = f.ValidFrom
	}
	return []any{
		f.ID, f.Subject, NormalizeKey(f.Subject), f.Predicate, NormalizeKey(f.Predicate),
		f.Value, NormalizeKey(f.Value),
		string(f.Status), f.Confidence, f.Importance, f.Confirmations,
		string(f.SourceType), f.SourceRef, f.SessionID, f.TurnID,
		now, validTo, now, now,
	}
}

// ConfirmFact 提高确认次数与置信度（重复说同一件事）。
func (r *Repo) ConfirmFact(ctx context.Context, id string, source SourceType) error {
	now := r.now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `
		UPDATE facts
		SET confirmations = confirmations + 1,
		    confidence = MIN(1.0, confidence + 0.05),
		    importance = MIN(1.0, importance + 0.03),
		    updated_at = ?
		WHERE id = ?`, now, id)
	return err
}

// DeleteFact 撤销一条记忆（软删除，保留审计链）。
func (r *Repo) DeleteFact(ctx context.Context, id, actor, reason string) error {
	now := r.now().UnixMilli()
	res, err := r.db.ExecContext(ctx, `
		UPDATE facts SET status = 'deleted', deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return r.LogEvent(ctx, Event{FactID: id, Action: "delete", Actor: actor, Detail: reason})
}

// TouchFact 记录一次访问（用于注入优先级排序）。
func (r *Repo) TouchFact(ctx context.Context, id string) error {
	now := r.now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `
		UPDATE facts SET access_count = access_count + 1, last_access_at = ? WHERE id = ?`, now, id)
	return err
}

// TouchFacts 批量记录访问次数。
//
// 画像卡每轮渲染几十行，逐行 UPDATE 会把一次读放大成几十次写。
// 批量版只发一条语句，且跳过空列表。
func (r *Repo) TouchFacts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := r.now().UnixMilli()

	// SQLite 参数上限 999，留一个位置给时间戳
	const batch = 900
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		part := ids[start:end]

		placeholders := strings.Repeat("?,", len(part))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, len(part)+1)
		args = append(args, now)
		for _, id := range part {
			args = append(args, id)
		}

		_, err := r.db.ExecContext(ctx, `
			UPDATE facts SET access_count = access_count + 1, last_access_at = ?
			WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
	}
	return nil
}

// ListFacts 列出事实（含历史版本），供 /memory 与导出使用。
func (r *Repo) ListFacts(ctx context.Context, status FactStatus, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + factColumns + ` FROM facts WHERE deleted_at IS NULL`
	args := []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

// ListAllFacts 列出事实，含已软删除的，供管理界面查看与恢复。
//
// 与 ListFacts 的区别：后者永远排除 deleted（因为召回不该看到死数据），
// 而管理页需要「看得到自己删过什么」。status 为空表示不过滤。
func (r *Repo) ListAllFacts(ctx context.Context, status FactStatus, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + factColumns + ` FROM facts WHERE 1=1`
	args := []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectFacts(rows)
}

// SetFactStatus 把事实改到指定状态，供管理界面确认或标疑。
//
// 只允许在 active / disputed / archived 之间迁移：deleted 必须走
// DeleteFact（它要同时写 deleted_at），恢复走 RestoreFact。
func (r *Repo) SetFactStatus(ctx context.Context, id string, status FactStatus, actor, reason string) error {
	switch status {
	case StatusActive, StatusDisputed, StatusArchived:
	default:
		return fmt.Errorf("memory: 不支持直接改到状态 %q", status)
	}

	now := r.now().UnixMilli()
	res, err := r.db.ExecContext(ctx,
		`UPDATE facts SET status = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(status), now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return r.LogEvent(ctx, Event{
		FactID: id, Action: "status:" + string(status), Actor: actor, Detail: reason,
	})
}

// RestoreFact 恢复一条软删除的事实。
func (r *Repo) RestoreFact(ctx context.Context, id, actor string) error {
	now := r.now().UnixMilli()
	res, err := r.db.ExecContext(ctx,
		`UPDATE facts SET status = 'active', deleted_at = NULL, updated_at = ?
		 WHERE id = ? AND deleted_at IS NOT NULL`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("事实 %s 不存在或未被删除: %w", id, ErrNotFound)
	}
	return r.LogEvent(ctx, Event{FactID: id, Action: "restore", Actor: actor})
}

// InsertEpisode 写入一条情节记忆。
func (r *Repo) InsertEpisode(ctx context.Context, e Episode) (string, error) {
	if e.Summary == "" {
		return "", errors.New("memory: 情节摘要不能为空")
	}
	if e.ID == "" {
		e.ID = newID("ep")
	}
	if e.Importance <= 0 {
		e.Importance = 0.5
	}
	now := r.now()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO episodes
			(id, session_id, turn_id, summary, keywords, entities, importance, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.SessionID, e.TurnID, e.Summary, e.Keywords, e.Entities,
		e.Importance, e.CreatedAt.UnixMilli())
	if err != nil {
		return "", fmt.Errorf("插入情节: %w", err)
	}
	return e.ID, nil
}

// RecentEpisodes 取最近的情节，供纯 SQL 召回。
func (r *Repo) RecentEpisodes(ctx context.Context, limit int) ([]Episode, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+episodeColumns+` FROM episodes
		WHERE archived_at IS NULL AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

// SearchEpisodes 用关键词做纯 SQL 召回。
//
// 不用 FTS：情节数量在个人规模下只有几百条，LIKE 足够快，
// 而且能直接按「命中词数」排序，比 BM25 更贴合这里的打分公式。
func (r *Repo) SearchEpisodes(ctx context.Context, terms []string, limit int) ([]Episode, error) {
	if len(terms) == 0 {
		return r.RecentEpisodes(ctx, limit)
	}
	if limit <= 0 {
		limit = 20
	}

	// keywords 列存的是空格分隔的词，用 LIKE 逐个匹配
	var conds []string
	args := []any{}
	for _, t := range terms {
		conds = append(conds, `(keywords LIKE ? OR entities LIKE ? OR summary LIKE ?)`)
		like := "%" + t + "%"
		args = append(args, like, like, like)
	}
	args = append(args, limit)

	query := `SELECT ` + episodeColumns + ` FROM episodes
		WHERE archived_at IS NULL AND deleted_at IS NULL AND (` + strings.Join(conds, " OR ") + `)
		ORDER BY created_at DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

// TouchEpisode 记录一次访问。
func (r *Repo) TouchEpisode(ctx context.Context, id string) error {
	return r.TouchEpisodes(ctx, []string{id})
}

// TouchEpisodes 批量记录情节访问。
//
// 与 TouchFacts 同样一次 UPDATE 覆盖所有 ID：召回命中后要记的
// 是几条情节，逐条写会把一次读放大成几次写。
func (r *Repo) TouchEpisodes(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := r.now().UnixMilli()

	const batch = 900
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		part := ids[start:end]

		placeholders := strings.Repeat("?,", len(part))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, len(part)+1)
		args = append(args, now)
		for _, id := range part {
			args = append(args, id)
		}

		if _, err := r.db.ExecContext(ctx, `
			UPDATE episodes SET access_count = access_count + 1, last_access_at = ?
			WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// ArchiveEpisode 归档一条情节（不再预召回）。
func (r *Repo) ArchiveEpisode(ctx context.Context, id string) error {
	now := r.now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `UPDATE episodes SET archived_at = ? WHERE id = ?`, now, id)
	return err
}

// Event 是一条记忆审计事件。
type Event struct {
	FactID    string
	EpisodeID string
	Action    string
	Actor     string
	Detail    string
	// CreatedAt 由查询填充；写入时由 LogEvent 用当前时间。
	CreatedAt time.Time
}

// LogEvent 写入审计日志。
func (r *Repo) LogEvent(ctx context.Context, e Event) error {
	if e.Actor == "" {
		e.Actor = "system"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO memory_events (fact_id, episode_id, action, actor, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.FactID, e.EpisodeID, e.Action, e.Actor, e.Detail, r.now().UnixMilli())
	return err
}

// Events 返回某条记忆的操作历史，供「这条记忆哪来的」溯源。
func (r *Repo) Events(ctx context.Context, factID string) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT fact_id, episode_id, action, actor, detail, created_at FROM memory_events
		WHERE fact_id = ? ORDER BY id`, factID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var created int64
		if err := rows.Scan(&e.FactID, &e.EpisodeID, &e.Action, &e.Actor, &e.Detail, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Stats 是记忆的规模快照。
type Stats struct {
	ActiveFacts int `json:"active_facts"`
	Disputed    int `json:"disputed"`
	Episodes    int `json:"episodes"`
	ArchivedEps int `json:"archived_episodes"`
	Candidates  int `json:"candidates"`
}

// Stats 统计各状态的数量。
func (r *Repo) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	// 一条语句算完五个计数：管理面板每次刷新都要这组数字，
	// 五次串行往返纯属白等。
	err := r.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM facts WHERE status='active' AND deleted_at IS NULL),
		   (SELECT COUNT(*) FROM facts WHERE status='disputed' AND deleted_at IS NULL),
		   (SELECT COUNT(*) FROM episodes WHERE deleted_at IS NULL AND archived_at IS NULL),
		   (SELECT COUNT(*) FROM episodes WHERE archived_at IS NOT NULL AND deleted_at IS NULL),
		   (SELECT COUNT(*) FROM memory_candidates)`).
		Scan(&s.ActiveFacts, &s.Disputed, &s.Episodes, &s.ArchivedEps, &s.Candidates)
	if err != nil {
		return Stats{}, err
	}
	return s, nil
}

const factColumns = `id, subject, subject_key, predicate, predicate_key, value, value_key,
	status, confidence, importance, confirmations, access_count, last_access_at,
	source_type, source_ref, session_id, turn_id,
	valid_from, valid_to, superseded_by, deleted_at, created_at, updated_at`

const episodeColumns = `id, session_id, turn_id, summary, keywords, entities,
	importance, access_count, last_access_at, created_at, archived_at, deleted_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanFact(sc rowScanner) (Fact, error) {
	var (
		f        Fact
		status   string
		source   string
		lastAcc  sql.NullInt64
		validTo  sql.NullInt64
		supersed sql.NullString
		deleted  sql.NullInt64
		validFrm int64
		created  int64
		updated  int64
	)
	err := sc.Scan(&f.ID, &f.Subject, &f.SubjectKey, &f.Predicate, &f.PredicateKey,
		&f.Value, &f.ValueKey, &status, &f.Confidence, &f.Importance, &f.Confirmations,
		&f.AccessCount, &lastAcc, &source, &f.SourceRef, &f.SessionID, &f.TurnID,
		&validFrm, &validTo, &supersed, &deleted, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	if err != nil {
		return Fact{}, err
	}

	f.Status = FactStatus(status)
	f.SourceType = SourceType(source)
	f.ValidFrom = time.UnixMilli(validFrm)
	f.CreatedAt = time.UnixMilli(created)
	f.UpdatedAt = time.UnixMilli(updated)
	if lastAcc.Valid {
		t := time.UnixMilli(lastAcc.Int64)
		f.LastAccessAt = &t
	}
	if validTo.Valid {
		t := time.UnixMilli(validTo.Int64)
		f.ValidTo = &t
	}
	if supersed.Valid {
		f.SupersededBy = supersed.String
	}
	if deleted.Valid {
		t := time.UnixMilli(deleted.Int64)
		f.DeletedAt = &t
	}
	return f, nil
}

func collectFacts(rows *sql.Rows) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func collectEpisodes(rows *sql.Rows) ([]Episode, error) {
	var out []Episode
	for rows.Next() {
		var (
			e        Episode
			lastAcc  sql.NullInt64
			archived sql.NullInt64
			deleted  sql.NullInt64
			created  int64
		)
		if err := rows.Scan(&e.ID, &e.SessionID, &e.TurnID, &e.Summary, &e.Keywords,
			&e.Entities, &e.Importance, &e.AccessCount, &lastAcc, &created,
			&archived, &deleted); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		if lastAcc.Valid {
			t := time.UnixMilli(lastAcc.Int64)
			e.LastAccessAt = &t
		}
		if archived.Valid {
			t := time.UnixMilli(archived.Int64)
			e.ArchivedAt = &t
		}
		if deleted.Valid {
			t := time.UnixMilli(deleted.Int64)
			e.DeletedAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
