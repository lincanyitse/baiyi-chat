package web

// 本地口令。
//
// 场景是单用户、本机运行。威胁模型不是「防远程攻击者」，而是
// 「同一台机器上的其他人打开浏览器就能看到我的对话」——所以
// 口令是一道本机隔离措施，不是完整的账号体系。
//
// 设计取舍：
//   - Argon2id 而不是 PBKDF2/bcrypt：官方推荐的密码哈希，抗 GPU
//   - 只存哈希，永不明文落库
//   - 登录成功后签发随机 32 字节 session token，存内存 + HttpOnly Cookie
//   - 不做 CSRF token：SameSite=Strict 已阻断跨站携带
//   - token 不落库：重启即需重新登录，单机场景可接受

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/argon2"
)

// 这些参数在单机验证场景下约 50–100ms，足够拖慢离线爆破又不影响体验。
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrNoPassword 表示尚未设置口令。
var ErrNoPassword = errors.New("尚未设置口令")

// ErrBadPassword 表示口令错误。
var ErrBadPassword = errors.New("口令错误")

// sessionTTL 是登录会话的有效期。
//
// 会话 token 的过期时间与 Cookie 的 MaxAge 必须是同一个值：
// 分成两处写死，改一处忘另一处就会出现「浏览器提前丢 Cookie」或
// 「Cookie 还在但 token 已失效」这种难排查的不一致。
const sessionTTL = 7 * 24 * time.Hour

// Auth 是口令与登录会话。
type Auth struct {
	db *sql.DB

	mu       sync.Mutex
	sessions map[string]time.Time // token -> 过期时间
	now      func() time.Time

	// enabled 缓存「是否已设置口令」。
	//
	// 这个值每个请求都要读（中间件据此决定是否拦截），而它只在
	// Setup/Change/Reset 三处变化——所有写路径都在本类型内，失效点
	// 是完备的。初值为未知，由第一次 Enabled() 查库填充。
	enabled atomic.Int32 // 0 = 未知，1 = 未启用，2 = 已启用
}

// NewAuth 构造口令服务。
func NewAuth(db *sql.DB) *Auth {
	return &Auth{
		db:       db,
		sessions: make(map[string]time.Time),
		now:      time.Now,
	}
}

// Enabled 报告是否已设置口令。未设置时所有请求都不需要登录。
//
// 结果缓存在 atomic 里：中间件每个请求都要问一次，而它只在
// Setup/Change/Reset 时变化。这三处写路径都在本类型内，缓存不会失真。
func (a *Auth) Enabled() (bool, error) {
	switch a.enabled.Load() {
	case 1:
		return false, nil
	case 2:
		return true, nil
	}

	var hash string
	err := a.db.QueryRow(`SELECT password_hash FROM web_auth WHERE id = 'default'`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		a.enabled.Store(1)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash == "" {
		a.enabled.Store(1)
		return false, nil
	}
	a.enabled.Store(2)
	return true, nil
}

// Setup 首次设置口令并直接签发会话令牌。
//
// 不返回 error 让调用方再 Login 一次：那会对同一个口令再跑一遍
// Argon2id（50–100ms），而刚刚才哈希过它。
func (a *Auth) Setup(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}

	enabled, err := a.Enabled()
	if err != nil {
		return "", err
	}
	if enabled {
		return "", errors.New("口令已设置；如需重置请使用 baiyi reset-password")
	}

	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := a.db.Exec(
		`INSERT INTO web_auth(id, password_hash, updated_at) VALUES ('default', ?, ?)
		 ON CONFLICT(id) DO UPDATE SET password_hash = excluded.password_hash, updated_at = excluded.updated_at`,
		hash, a.now().Unix()); err != nil {
		return "", err
	}
	a.enabled.Store(2)
	return a.issue()
}

// Change 修改口令并签发新令牌（需验证旧口令）。
//
// 同样不返回 error 让调用方再 Login：Change 内部已经算过两次
// Argon2id（验旧、哈希新），第三次纯属浪费。
func (a *Auth) Change(oldPassword, newPassword string) (string, error) {
	if err := a.Verify(oldPassword); err != nil {
		return "", err
	}
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if _, err := a.db.Exec(`UPDATE web_auth SET password_hash = ?, updated_at = ? WHERE id = 'default'`,
		hash, a.now().Unix()); err != nil {
		return "", err
	}

	// 改口令要让所有已登录会话失效，再给当前浏览器签发新的
	a.dropAllSessions()
	return a.issue()
}

// Reset 清空口令（供 reset-password 子命令使用）。
func (a *Auth) Reset() error {
	if _, err := a.db.Exec(`DELETE FROM web_auth WHERE id = 'default'`); err != nil {
		return err
	}
	a.enabled.Store(1)
	a.dropAllSessions()
	return nil
}

// Verify 校验口令。
func (a *Auth) Verify(password string) error {
	var hash string
	err := a.db.QueryRow(`SELECT password_hash FROM web_auth WHERE id = 'default'`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hash == "") {
		return ErrNoPassword
	}
	if err != nil {
		return err
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadPassword
	}
	return nil
}

// Login 校验口令并签发会话 token。
func (a *Auth) Login(password string) (string, error) {
	if err := a.Verify(password); err != nil {
		return "", err
	}
	return a.issue()
}

// issue 生成新的会话 token。
func (a *Auth) issue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	a.mu.Lock()
	now := a.now()
	a.sweepLocked(now)
	a.sessions[token] = now.Add(sessionTTL)
	a.mu.Unlock()
	return token, nil
}

// Valid 报告 token 是否有效。
func (a *Auth) Valid(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	// 顺便清理：过期项不该只靠「下次登录」这个偶然事件回收。
	// 一次遍历是 O(存活会话数)，单用户下只有个位数。
	a.sweepLocked(now)

	exp, ok := a.sessions[token]
	if !ok {
		return false
	}
	return !now.After(exp)
}

// sweepLocked 删除所有过期会话。调用方必须持有 a.mu。
func (a *Auth) sweepLocked(now time.Time) {
	for t, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, t)
		}
	}
}

// Logout 使 token 失效。
func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// dropAllSessions 在口令变更后让所有已登录会话失效。
func (a *Auth) dropAllSessions() {
	a.mu.Lock()
	clear(a.sessions)
	a.mu.Unlock()
}

// validatePassword 是口令强度下限。
//
// 不设复杂度规则（大小写混排之类）：那类规则实际收益很低，
// 只会逼出 Password1!。长度是最有效的单一指标。
func validatePassword(p string) error {
	if len([]rune(strings.TrimSpace(p))) < 8 {
		return errors.New("口令至少需要 8 个字符")
	}
	return nil
}

// hashPassword 生成 PHC 格式的 Argon2id 哈希。
//
// 格式：$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>
// 参数随哈希一起存，将来调参能兼容旧记录。
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword 比对口令与 PHC 哈希。
func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("不支持的哈希格式")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, errors.New("哈希版本解析失败")
	}
	if version != argon2.Version {
		return false, errors.New("哈希版本不兼容")
	}

	var memory, iters uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iters, &threads); err != nil {
		return false, errors.New("哈希参数解析失败")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("盐解析失败")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errors.New("哈希解析失败")
	}

	got := argon2.IDKey([]byte(password), salt, iters, memory, threads, uint32(len(want)))
	// 常量时间比较，避免计时侧信道
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
