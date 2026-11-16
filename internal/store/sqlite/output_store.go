// Package sqlite 实现 tool.OutputStore：内容寻址的大结果存储。
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

// OutputStore 把被截断的工具结果存进 tool_output_store 表。
//
// 句柄是内容哈希的前 16 位十六进制：相同内容得到相同句柄，
// 重复保存不会产生重复行。
type OutputStore struct {
	db *sqlite.DB
}

// NewOutputStore 构造存储。
func NewOutputStore(db *sqlite.DB) *OutputStore { return &OutputStore{db: db} }

// Save 保存内容并返回句柄。
func (s *OutputStore) Save(ctx context.Context, turnID, callID, content string) (string, error) {
	sum := sha256.Sum256([]byte(content))
	handle := hex.EncodeToString(sum[:8])

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tool_output_store(handle, content_hash, size, encoding, data, created_at)
		 VALUES (?, ?, ?, 'utf8', ?, ?)
		 ON CONFLICT(handle) DO NOTHING`,
		handle, hex.EncodeToString(sum[:]), len(content), []byte(content),
		nowUnix())
	if err != nil {
		return "", fmt.Errorf("保存工具输出: %w", err)
	}
	return handle, nil
}

// Load 按句柄读取内容。offset 为字节偏移，limit 为最大返回字节数（0 表示不限）。
func (s *OutputStore) Load(ctx context.Context, handle string, offset, limit int) (string, int, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM tool_output_store WHERE handle = ?`, handle).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("句柄 %q 不存在或已过期", handle)
		}
		return "", 0, err
	}

	total := len(data)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return "", total, nil
	}
	chunk := data[offset:]
	if limit > 0 && len(chunk) > limit {
		chunk = chunk[:limit]
	}
	// 保证不切断多字节字符
	for len(chunk) > 0 && !utf8.Valid(chunk) {
		chunk = chunk[:len(chunk)-1]
	}
	return string(chunk), total, nil
}

// Delete 删除句柄（用于清理任务）。
func (s *OutputStore) Delete(ctx context.Context, handle string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM tool_output_store WHERE handle = ?`, handle)
	return err
}

// GC 删除超过 maxAge 秒的记录，返回删除数量。
func (s *OutputStore) GC(ctx context.Context, maxAgeSeconds int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM tool_output_store WHERE created_at < ?`,
		nowUnix()-maxAgeSeconds)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nowUnix() int64 { return timeNow().Unix() }

// timeNow 便于测试替换。
var timeNow = time.Now

// SaveSnapshot 持久化一条上下文快照。
//
// 段正文超过 2KB 时进 blobs，快照里只留哈希，避免快照表无限膨胀。
func (s *OutputStore) SaveSnapshot(ctx context.Context, snapID, turnID string, step int,
	model, segmentsJSON, budgetJSON, breakpoints, totalsJSON, prefixHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO context_snapshots
		 (id, turn_id, step, model, segments_json, budget_json,
		  cache_breakpoints, totals_json, prefix_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		snapID, turnID, step, model, segmentsJSON, budgetJSON,
		breakpoints, totalsJSON, prefixHash, nowUnix())
	return err
}

// LatestSnapshot 返回某轮次最近一条快照的 segments_json 与 budget_json。
func (s *OutputStore) LatestSnapshot(ctx context.Context, turnID string) (segments, budget string, step int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT segments_json, budget_json, step FROM context_snapshots
		 WHERE turn_id = ? ORDER BY step DESC LIMIT 1`, turnID).
		Scan(&segments, &budget, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, fmt.Errorf("该轮次没有上下文快照")
	}
	return segments, budget, step, err
}
