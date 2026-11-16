// Package sqlite 提供数据库连接与版本化迁移。
//
// 迁移使用内嵌 SQL + PRAGMA user_version。每次迁移前自动备份数据库文件。
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB 包装 *sql.DB 并保留路径信息以便备份。
type DB struct {
	*sql.DB
	Path string
}

// Open 打开（必要时创建）数据库并应用迁移。
// dbPath 为 ":memory:" 时使用内存库（测试用，无迁移备份）。
func Open(dbPath string) (*DB, error) {
	memory := dbPath == ":memory:"
	if !memory {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	if memory {
		// 用唯一名字的内存库。裸 `file::memory:?cache=shared` 会让**所有**
		// 打开的连接共享同一个库——多个测试并行跑时互相看到对方的表与数据，
		// 表现为「单独跑通过、全量跑失败」。
		dsn = fmt.Sprintf("file:memdb%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
			memSeq.Add(1))
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	raw.SetMaxOpenConns(1) // SQLite 单写者；个人规模下串行足够且避免锁竞争
	raw.SetMaxIdleConns(1)
	raw.SetConnMaxLifetime(0)

	if !memory {
		if _, err := raw.ExecContext(context.Background(), `PRAGMA journal_mode=WAL`); err != nil {
			raw.Close()
			return nil, fmt.Errorf("enable WAL: %w", err)
		}
	}

	return &DB{DB: raw, Path: dbPath}, nil
}

// Migrate 按版本顺序应用尚未执行的迁移。返回本次执行的数量。
func (d *DB) Migrate() (int, error) {
	var current int
	if err := d.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&current); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}

	all, err := loadMigrations()
	if err != nil {
		return 0, err
	}

	var pending []migration
	for _, m := range all {
		if m.version > current {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	if d.Path != ":memory:" {
		if _, err := d.backup(); err != nil {
			return 0, fmt.Errorf("pre-migrate backup: %w", err)
		}
	}

	applied := 0
	for _, m := range pending {
		tx, err := d.BeginTx(context.Background(), nil)
		if err != nil {
			return applied, fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := execMigration(tx, m.sql); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		// PRAGMA 不支持参数占位符，version 来自文件名解析，是受控整数。
		if _, err := tx.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA user_version=%d`, m.version)); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("set user_version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("commit migration %d: %w", m.version, err)
		}
		applied++
	}
	return applied, nil
}

// execMigration 逐条执行迁移语句，并把 ADD COLUMN 变成幂等操作。
//
// SQLite 不支持 ADD COLUMN IF NOT EXISTS。若迁移因故重跑（版本被回退、
// 或进程在上次提交前崩溃），重复加列会直接失败，因此这里先查列是否存在。
func execMigration(tx *sql.Tx, script string) error {
	for _, stmt := range splitSQL(script) {
		table, column, ok := parseAddColumn(stmt)
		if !ok {
			if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
				return err
			}
			continue
		}
		exists, err := columnExists(tx, table, column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			return err
		}
	}
	return nil
}

// splitSQL 按分号切分脚本，忽略注释与空语句。
func splitSQL(script string) []string {
	var out []string
	var cur strings.Builder

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, strings.TrimSuffix(s, ";"))
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, strings.TrimSuffix(s, ";"))
	}
	return out
}

// parseAddColumn 识别 "ALTER TABLE x ADD COLUMN y ..." 形式的语句。
func parseAddColumn(stmt string) (table, column string, ok bool) {
	fields := strings.Fields(stmt)
	if len(fields) < 6 {
		return "", "", false
	}
	if !strings.EqualFold(fields[0], "ALTER") || !strings.EqualFold(fields[1], "TABLE") {
		return "", "", false
	}
	if !strings.EqualFold(fields[3], "ADD") || !strings.EqualFold(fields[4], "COLUMN") {
		return "", "", false
	}
	return strings.TrimSpace(fields[2]), strings.TrimSpace(fields[5]), true
}

func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	// PRAGMA table_info 不支持参数占位符；table 来自内嵌迁移脚本，是受控标识符。
	rows, err := tx.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations 读取内嵌的 migrations/NNNN_name.sql 并按版本排序。
func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		verStr, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration 文件名必须以版本号开头: %s", e.Name())
		}
		ver, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("migration 版本号非法 %q: %w", verStr, err)
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: ver, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// backup 把当前数据库文件复制到 backups/ 下，返回备份路径。
func (d *DB) backup() (string, error) {
	dir := filepath.Join(filepath.Dir(d.Path), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s.%s.bak", filepath.Base(d.Path), time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)

	src, err := os.Open(d.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // 首次迁移，无文件可备份
		}
		return "", err
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return "", err
	}
	return dst, nil
}

// Version 返回当前 schema 版本，供测试与诊断使用。
func (d *DB) Version() (int, error) {
	var v int
	err := d.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v)
	return v, err
}

// memSeq 给每个内存库分配唯一名字，保证测试之间互不干扰。
var memSeq atomic.Uint64
