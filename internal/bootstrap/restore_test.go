package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	store "github.com/lincanyitse/baiyi-chat/internal/store/sqlite"
)

func openSQLite(path string) (*sqlite.DB, error) {
	return sqlite.Open(path)
}

// TestRestorePreservesWALData 是这次发现的缺陷的回归测试。
//
// WAL 模式下最近的写入可能还在 -wal 里。只改名 .db 而不带上 -wal，
// 「保留的旧库」会是一个少了最后几轮对话的残缺文件——回退时救不回来。
//
// 正常 Close 会 checkpoint 并删掉 -wal，所以这里手动造一个 -wal
// 来模拟「进程异常退出」留下的状态。
func TestRestorePreservesWALData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "baiyi.db")

	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	repo := store.New(db)
	ctx := context.Background()
	if err := repo.CreateSession(ctx, session.Session{ID: "s1", Title: "重要会话"}); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(dir, "backup.db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+escapeSQLString(backupPath)+"'"); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 模拟异常退出留下的 WAL
	if err := os.WriteFile(dbPath+"-wal", []byte("fake-wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(backupPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousDB == "" {
		t.Fatal("应保留原数据库")
	}

	if _, err := os.Stat(res.PreviousDB + "-wal"); err != nil {
		t.Errorf("原库的 -wal 应一并保留，否则回退会丢数据: %v", err)
	}
	// 目标库旁边不该残留学旧 WAL，否则新库会读到旧数据
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		t.Error("恢复后目标库旁不该有旧的 -wal")
	}
}

// TestRestoreKeepsOldDB 恢复后旧库仍在，可以回退。
func TestRestoreKeepsOldDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "baiyi.db")

	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if _, err := db.ExecContext(context.Background(), "VACUUM INTO '"+escapeSQLString(backupPath)+"'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(backupPath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.PreviousDB); err != nil {
		t.Errorf("原数据库应保留: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("恢复后目标库应存在: %v", err)
	}
}

// TestRestoreRejectsInvalidBackup 非 SQLite 文件必须被拒绝，且不能动现有库。
func TestRestoreRejectsInvalidBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "baiyi.db")

	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(dir, "not-a-db.db")
	if err := os.WriteFile(bad, []byte("这不是数据库"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(bad, dir); err == nil {
		t.Fatal("坏文件应被拒绝")
	}

	// 现有库必须完好
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("校验失败时不该动现有库: %v", err)
	}
	if _, err := os.Stat(dbPath + ".before-restore"); err == nil {
		t.Error("校验失败时不该产生保留文件")
	}
}
