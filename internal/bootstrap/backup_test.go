package bootstrap

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
)

func newBackupApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "baiyi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	app := &App{
		DB:  db,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app.Config.DataDir = dir
	return app, dir
}

// TestBackupIncludesUncheckpointedWrites 是这次的核心断言：
// 写入后立刻备份，备份里必须能看到最后一条。
//
// 这正是「只复制 .db」会失败的地方——WAL 里还没落盘的内容会被漏掉。
func TestBackupIncludesUncheckpointedWrites(t *testing.T) {
	app, _ := newBackupApp(t)
	ctx := context.Background()

	repo := store.New(app.DB)
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "刚写的会话"}); err != nil {
		t.Fatal(err)
	}

	info, err := app.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 打开备份文件，确认会话在里面
	bdb, err := sql.Open("sqlite", info.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	var title string
	if err := bdb.QueryRow("SELECT title FROM sessions WHERE id = 's1'").Scan(&title); err != nil {
		t.Fatalf("备份里找不到刚写入的会话: %v", err)
	}
	if title != "刚写的会话" {
		t.Errorf("标题不对: %q", title)
	}
}

// TestBackupIsStandalone 备份不该依赖 -wal：拷到别处也能打开。
func TestBackupIsStandalone(t *testing.T) {
	app, _ := newBackupApp(t)
	ctx := context.Background()

	repo := store.New(app.DB)
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "x"}); err != nil {
		t.Fatal(err)
	}

	info, err := app.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 复制到另一个目录，模拟「备份被拿走」
	other := t.TempDir()
	moved := filepath.Join(other, "moved.db")
	copyForTest(t, info.Path, moved)

	bdb, err := sql.Open("sqlite", moved)
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()

	var n int
	if err := bdb.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("拷贝后的备份打不开（可能依赖了 -wal）: %v", err)
	}
	if n != 1 {
		t.Errorf("会话数应为 1，实际 %d", n)
	}
}

// TestListBackups 按时间倒序返回，且包含迁移前的 .bak。
func TestListBackups(t *testing.T) {
	app, _ := newBackupApp(t)

	dir := filepath.Join(app.Config.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 显式设置 mtime：同一秒内创建的两个文件排序不稳定
	files := []struct {
		name string
		at   time.Time
	}{
		{"baiyi-20260101-000000.db", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"baiyi-20260102-000000.db", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.at, f.at); err != nil {
			t.Fatal(err)
		}
	}

	all, err := app.ListBackups()
	if err != nil {
		t.Fatal(err)
	}

	// 迁移前自动备份的 .bak 也算——用户应该能看到它
	var dbCount int
	for _, b := range all {
		if filepath.Ext(b.Path) == ".db" {
			dbCount++
		}
	}
	if dbCount != 2 {
		t.Fatalf("应有 2 个 .db 备份，实际 %d（总共 %d 个）", dbCount, len(all))
	}

	// 最新的 .db 排在最前
	var newest string
	for _, b := range all {
		if filepath.Ext(b.Path) == ".db" {
			newest = filepath.Base(b.Path)
			break
		}
	}
	if newest != "baiyi-20260102-000000.db" {
		t.Errorf("应按时间倒序，最新的 .db 是 %s", newest)
	}
}

func copyForTest(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}
