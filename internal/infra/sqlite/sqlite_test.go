package sqlite

import (
	"path/filepath"
	"testing"
)

func TestMigrateCreatesTables(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	n, err := db.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 迁移脚本数量随里程碑增长；断言「至少执行了 M0 的那一个」。
	if n < 1 {
		t.Fatalf("首次迁移应至少执行 1 个脚本，实际 %d", n)
	}

	v, err := db.Version()
	if err != nil || v < 1 {
		t.Fatalf("version = %d, err = %v; 期望 >= 1", v, err)
	}

	for _, table := range []string{
		"sessions", "messages", "turns", "llm_calls", "prices",
		"tool_results", "tool_output_store",
	} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("表 %s 不存在: %v", table, err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	n, err := db.Migrate()
	if err != nil {
		t.Fatalf("重复迁移出错: %v", err)
	}
	if n != 0 {
		t.Fatalf("重复迁移不应执行任何脚本，实际 %d", n)
	}
}

func TestMigrateBacksUpOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// 把版本回退，模拟需要再次迁移的旧库
	db2, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if _, err := db2.Exec(`PRAGMA user_version=0`); err != nil {
		t.Fatal(err)
	}

	if _, err := db2.Migrate(); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(dir, "backups", "*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) == 0 {
		t.Fatal("升级迁移前应生成备份文件")
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	db, err := Open(pathInTemp(t, "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO messages(id, session_id, seq, role, content_json, created_at)
	                  VALUES ('m1', 'nonexistent-session', 1, 'user', '{}', 0)`)
	if err == nil {
		t.Fatal("外键约束未生效：插入了引用不存在 session 的消息")
	}
}

func pathInTemp(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}
