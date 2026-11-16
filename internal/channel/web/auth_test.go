package web

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/infra/sqlite"
)

func newAuth(t *testing.T) *Auth {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewAuth(db.DB)
}

func TestAuthDisabledByDefault(t *testing.T) {
	a := newAuth(t)
	enabled, err := a.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("初始状态不应启用口令")
	}
}

func TestAuthSetupAndLogin(t *testing.T) {
	a := newAuth(t)

	if _, err := a.Setup("correct horse battery"); err != nil {
		t.Fatal(err)
	}

	enabled, _ := a.Enabled()
	if !enabled {
		t.Fatal("设置后应启用")
	}

	token, err := a.Login("correct horse battery")
	if err != nil {
		t.Fatalf("正确口令应能登录: %v", err)
	}
	if !a.Valid(token) {
		t.Error("新签发的 token 应有效")
	}

	if _, err := a.Login("wrong password"); !errors.Is(err, ErrBadPassword) {
		t.Errorf("错误口令应返回 ErrBadPassword，得到 %v", err)
	}
}

func TestAuthRejectsShortPassword(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("short"); err == nil {
		t.Error("过短的口令应被拒绝")
	}
}

// 口令不能明文落库——这是整个方案的前提。
func TestAuthStoresHashOnly(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	a := NewAuth(db.DB)
	const password = "my-secret-passphrase"
	if _, err := a.Setup(password); err != nil {
		t.Fatal(err)
	}

	var hash string
	if err := db.DB.QueryRow(`SELECT password_hash FROM web_auth WHERE id='default'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("口令以明文形式存在于数据库中")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("哈希格式不对: %s", hash)
	}
}

// 同一个口令两次哈希必须不同（随机盐）。
func TestAuthHashIsSalted(t *testing.T) {
	h1, err := hashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("相同口令产生了相同哈希，说明没有随机盐")
	}
}

func TestAuthRejectsDoubleSetup(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("first-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Setup("second-password"); err == nil {
		t.Error("重复设置应被拒绝，否则任何人都能改口令")
	}
}

func TestAuthLogoutInvalidatesToken(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("password123"); err != nil {
		t.Fatal(err)
	}
	token, _ := a.Login("password123")

	if !a.Valid(token) {
		t.Fatal("登录后应有效")
	}
	a.Logout(token)
	if a.Valid(token) {
		t.Error("登出后 token 应失效")
	}
}

func TestAuthChangeRequiresOldPassword(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("old-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Change("wrong-old", "new-password"); !errors.Is(err, ErrBadPassword) {
		t.Errorf("旧口令错误时应拒绝，得到 %v", err)
	}

	if _, err := a.Change("old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Login("old-password"); err == nil {
		t.Error("旧口令应失效")
	}
	if _, err := a.Login("new-password"); err != nil {
		t.Errorf("新口令应可用: %v", err)
	}
}

// 改口令要让已登录的会话全部失效。
func TestAuthChangeDropsSessions(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("old-password"); err != nil {
		t.Fatal(err)
	}
	token, _ := a.Login("old-password")

	if _, err := a.Change("old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if a.Valid(token) {
		t.Error("改口令后旧会话应失效")
	}
}

func TestAuthResetClearsPassword(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("password123"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	enabled, _ := a.Enabled()
	if enabled {
		t.Error("重置后不应启用口令")
	}
	if _, err := a.Setup("new-password"); err != nil {
		t.Errorf("重置后应能重新设置: %v", err)
	}
}

func TestAuthSessionExpires(t *testing.T) {
	a := newAuth(t)
	if _, err := a.Setup("password123"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	a.now = func() time.Time { return now }

	token, _ := a.Login("password123")
	if !a.Valid(token) {
		t.Fatal("刚签发的 token 应有效")
	}

	a.now = func() time.Time { return now.Add(sessionTTL + time.Minute) }
	if a.Valid(token) {
		t.Error("过期 token 应失效")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"plaintext",
		"$argon2id$v=19$m=65536,t=1,p=4$onlyfour",
		"$bcrypt$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA",
	}
	for _, c := range cases {
		if ok, err := verifyPassword("x", c); ok || err == nil {
			t.Errorf("畸形哈希 %q 应返回错误", c)
		}
	}
}

func TestValidatePasswordTrimsSpace(t *testing.T) {
	// 全是空白应视为过短
	if err := validatePassword("        "); err == nil {
		t.Error("纯空白口令应被拒绝")
	}
}
