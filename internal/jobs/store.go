package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store 是任务的持久化层。
type Store struct {
	db *sql.DB
	// now 可在测试中替换，避免用真实时钟做时间旅行。
	now func() time.Time
}

// NewStore 构造存储层。
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// ErrNotFound 表示任务不存在。
var ErrNotFound = errors.New("jobs: 任务不存在")

// Enqueue 提交任务。dedupeKey 非空且已有同 kind 的 pending/running 任务时，
// 返回既有任务并返回 created=false。
func (s *Store) Enqueue(ctx context.Context, in Enqueue) (Job, bool, error) {
	if in.Kind == "" {
		return Job{}, false, errors.New("jobs: Kind 不能为空")
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	payload := []byte("{}")
	if in.Payload != nil {
		raw, err := json.Marshal(in.Payload)
		if err != nil {
			return Job{}, false, fmt.Errorf("序列化任务载荷: %w", err)
		}
		payload = raw
	}

	now := s.now()
	runAfter := now
	if in.RunAfter != nil {
		runAfter = *in.RunAfter
	}

	if in.DedupeKey != "" {
		existing, err := s.findByDedupeKey(ctx, in.Kind, in.DedupeKey)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Job{}, false, err
		}
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (kind, payload, dedupe_key, status, priority, attempts, max_attempts, run_after, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, 0, ?, ?, ?, ?)`,
		in.Kind, string(payload), in.DedupeKey, in.Priority, maxAttempts,
		runAfter.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		// 并发下唯一索引会拦下重复插入，退回去读既有任务
		if in.DedupeKey != "" && isUniqueViolation(err) {
			existing, findErr := s.findByDedupeKey(ctx, in.Kind, in.DedupeKey)
			if findErr == nil {
				return existing, false, nil
			}
		}
		return Job{}, false, fmt.Errorf("插入任务: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return Job{}, false, err
	}
	job, err := s.Get(ctx, id)
	return job, true, err
}

func (s *Store) findByDedupeKey(ctx context.Context, kind, key string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, payload, dedupe_key, status, priority, attempts, max_attempts, last_error,
		       run_after, created_at, updated_at, started_at, finished_at
		FROM jobs WHERE kind = ? AND dedupe_key = ? AND status IN ('pending', 'running')
		LIMIT 1`, kind, key)
	return scanJob(row)
}

// Get 按 ID 取任务。
func (s *Store) Get(ctx context.Context, id int64) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, payload, dedupe_key, status, priority, attempts, max_attempts, last_error,
		       run_after, created_at, updated_at, started_at, finished_at
		FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

// Claim 取一条可执行任务并原子地标记为 running。
//
// 多 worker 并发调用时，靠 UPDATE ... WHERE status='pending' 的影响行数
// 来决定归属——只有影响 1 行的那个 worker 拿到任务。
func (s *Store) Claim(ctx context.Context, now time.Time) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE status = 'pending' AND run_after <= ?
		ORDER BY priority DESC, id
		LIMIT 1`, now.UnixMilli())

	var id int64
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'running', started_at = ?, updated_at = ?, attempts = attempts + 1
		WHERE id = ? AND status = 'pending'`, now.UnixMilli(), now.UnixMilli(), id)
	if err != nil {
		return Job{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if affected == 0 {
		// 被别的 worker 抢先；调用方应立刻再试一次
		return Job{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Succeed 把任务标记为完成。
func (s *Store) Succeed(ctx context.Context, id int64) error {
	now := s.now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'done', finished_at = ?, updated_at = ?, last_error = ''
		WHERE id = ?`, now, now, id)
	return err
}

// Fail 记录一次失败。剩余次数用尽时转 failed，否则重新排队并退避。
//
// 退避采用指数增长：attempts 次失败后等待 base * 2^(attempts-1)，
// 上限 10 分钟。嵌入服务短暂抖动通常几秒内恢复，而长时间宕机
// 也不该把队列刷爆。
func (s *Store) Fail(ctx context.Context, id int64, cause error, canceled bool) error {
	var attempts, maxAttempts int
	err := s.db.QueryRowContext(ctx, `SELECT attempts, max_attempts FROM jobs WHERE id = ?`, id).
		Scan(&attempts, &maxAttempts)
	if err != nil {
		return err
	}

	now := s.now()
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}

	if canceled || attempts >= maxAttempts {
		status := StatusFailed
		if canceled {
			status = StatusCanceled
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, finished_at = ?, updated_at = ?, last_error = ?
			WHERE id = ?`, string(status), now.UnixMilli(), now.UnixMilli(), msg, id)
		return err
	}

	backoff := min(time.Duration(1<<uint(attempts-1))*time.Second, 10*time.Minute)
	_, err = s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'pending', updated_at = ?, last_error = ?, run_after = ?
		WHERE id = ?`, now.UnixMilli(), msg, now.Add(backoff).UnixMilli(), id)
	return err
}

// RequeueStale 把「进程崩溃时正在跑」的任务放回队列。
//
// 启动时调用一次即可。没有这一步，崩溃后任务会永远卡在 running——
// 唯一的补救是人工改库，而用户不会知道要这么做。
func (s *Store) RequeueStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	now := s.now()
	cutoff := now.Add(-olderThan).UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'pending', updated_at = ?, started_at = NULL
		WHERE status = 'running' AND started_at < ?`, now.UnixMilli(), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Cancel 取消一个待执行任务。
func (s *Store) Cancel(ctx context.Context, id int64) error {
	now := s.now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('pending', 'running')`, now, now, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Stats 是队列状态快照，供 /jobs 命令与诊断使用。
type Stats struct {
	Pending  int64 `json:"pending"`
	Running  int64 `json:"running"`
	Done     int64 `json:"done"`
	Failed   int64 `json:"failed"`
	Canceled int64 `json:"canceled"`
}

// Stats 统计各状态任务数。
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	var st Stats
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return Stats{}, err
		}
		switch Status(status) {
		case StatusPending:
			st.Pending = n
		case StatusRunning:
			st.Running = n
		case StatusDone:
			st.Done = n
		case StatusFailed:
			st.Failed = n
		case StatusCanceled:
			st.Canceled = n
		}
	}
	return st, rows.Err()
}

// ListRecent 返回最近的任务，按 ID 倒序。kind 为空表示不过滤。
func (s *Store) ListRecent(ctx context.Context, kind string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, kind, payload, dedupe_key, status, priority, attempts, max_attempts, last_error,
		       run_after, created_at, updated_at, started_at, finished_at
		FROM jobs`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// PurgeFinished 删除已结束且早于 before 的任务。
func (s *Store) PurgeFinished(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM jobs WHERE status IN ('done', 'failed', 'canceled') AND finished_at < ?`,
		before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

func scanJob(sc scanner) (Job, error) {
	var (
		j         Job
		payload   string
		status    string
		runAfter  int64
		createdAt int64
		updatedAt int64
		startedAt sql.NullInt64
		finished  sql.NullInt64
	)
	err := sc.Scan(&j.ID, &j.Kind, &payload, &j.DedupeKey, &status, &j.Priority,
		&j.Attempts, &j.MaxAttempts, &j.LastError,
		&runAfter, &createdAt, &updatedAt, &startedAt, &finished)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}

	j.Payload = json.RawMessage(payload)
	j.Status = Status(status)
	j.RunAfter = time.UnixMilli(runAfter)
	j.CreatedAt = time.UnixMilli(createdAt)
	j.UpdatedAt = time.UnixMilli(updatedAt)
	if startedAt.Valid {
		t := time.UnixMilli(startedAt.Int64)
		j.StartedAt = &t
	}
	if finished.Valid {
		t := time.UnixMilli(finished.Int64)
		j.FinishedAt = &t
	}
	return j, nil
}

// isUniqueViolation 判断错误是否为唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
