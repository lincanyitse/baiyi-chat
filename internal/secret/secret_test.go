package secret

import (
	"context"
	"errors"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

// newStore 建一个临时库并初始化存储。
func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DB: db.DB, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "anthropic", "sk-ant-secret-value"); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Get(ctx, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "sk-ant-secret-value" {
		t.Fatalf("往返失败: ok=%v got=%q", ok, got)
	}
}

func TestGetMissing(t *testing.T) {
	s := newStore(t)
	_, ok, err := s.Get(context.Background(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("未设置的 provider 不该返回 ok")
	}
}

func TestPutOverwrites(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "p", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "p", "new"); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Fatalf("应覆盖为新值，实际 %q", got)
	}
}

func TestPutEmptyDeletes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "p", "value"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Has(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("空串应等同于删除")
	}
}

func TestHasDoesNotDecrypt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "p", "value"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Has(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("应报告已存有密钥")
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "p", "value"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(ctx, "p"); ok {
		t.Fatal("删除后不该还有密钥")
	}
	// 删除不存在的记录不是错误
	if err := s.Delete(ctx, "p"); err != nil {
		t.Fatalf("重复删除应无错: %v", err)
	}
}

func TestResetClearsAll(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "a", "key-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "b", "key-b"); err != nil {
		t.Fatal(err)
	}

	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a", "b"} {
		if ok, _ := s.Has(ctx, name); ok {
			t.Fatalf("重置后 %s 不该还有密钥", name)
		}
	}

	// 重置后仍可正常使用（新的数据密钥）
	if err := s.Put(ctx, "c", "key-c"); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if got != "key-c" {
		t.Fatalf("重置后往返失败: %q", got)
	}
}

func TestProtectionReported(t *testing.T) {
	s := newStore(t)
	if s.Protection() == "" {
		t.Fatal("应报告保护方式")
	}
}

// TestCiphertextIsNotPlaintext 确认库里躺的不是明文。
func TestCiphertextIsNotPlaintext(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	s, err := New(Options{DB: db.DB, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const plaintext = "sk-ant-should-not-appear"
	if err := s.Put(context.Background(), "p", plaintext); err != nil {
		t.Fatal(err)
	}

	var enc []byte
	if err := db.QueryRow(`SELECT key_encrypted FROM provider_secrets WHERE provider_name = 'p'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if string(enc) == plaintext {
		t.Fatal("库里存了明文")
	}
	for i := 0; i+len(plaintext) <= len(enc); i++ {
		if string(enc[i:i+len(plaintext)]) == plaintext {
			t.Fatal("密文里出现了明文片段")
		}
	}
}

// TestKeyLost 模拟「数据密钥解不开」：把 secret_meta 换成无效 blob。
func TestKeyLost(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()

	if _, err := New(Options{DB: db.DB, DataDir: dataDir}); err != nil {
		t.Fatal(err)
	}

	// 破坏数据密钥记录
	if _, err := db.Exec(`UPDATE secret_meta SET wrapped_dek = x'deadbeef' WHERE id = 'dek'`); err != nil {
		t.Fatal(err)
	}

	_, err = New(Options{DB: db.DB, DataDir: dataDir})
	if !errors.Is(err, ErrKeyLost) {
		t.Fatalf("应返回 ErrKeyLost，实际: %v", err)
	}
}
