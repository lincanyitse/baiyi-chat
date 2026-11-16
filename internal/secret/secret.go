// Package secret 加密保存 Provider 的 API Key。
//
// 分两层：数据密钥（DEK）用一个平台相关的保护机制包裹后存库，
// API Key 用 DEK 做 AES-256-GCM。这样每次读写只需一次 AES 运算，
// 而系统级保护（DPAPI）的开销只在启动时付一次。
//
// 平台差异：
//   - Windows：DPAPI，绑定当前用户。换用户或换机器解不开。
//   - 其他平台：密钥文件与 inode + 设备号绑定。文件被复制到别处、
//     被替换、内容被改动都解不开；同设备移动仍可用。
//
// 这个包不打印任何密钥。错误信息里也不含明文。
package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Protection 标识数据密钥的保护方式，写入 secret_meta 供诊断使用。
type Protection string

const (
	ProtectionDPAPI       Protection = "dpapi"
	ProtectionFileBinding Protection = "file-binding"
	ProtectionNone        Protection = "none" // 仅测试用
)

// ErrKeyLost 表示数据密钥存在但解不开：换过用户、换过机器，
// 或绑定文件被替换。这时所有已存的 API Key 都失效，只能重置。
var ErrKeyLost = errors.New("数据密钥解不开：可能换了系统用户、换了机器，或密钥文件被替换")

// ErrNoDEK 表示还没初始化过数据密钥。
var ErrNoDEK = errors.New("尚未初始化数据密钥")

const dekID = "dek"
const dekSize = 32

// Store 管理数据密钥与 Provider 密钥的加解密。
//
// 数据密钥在进程内缓存，加解密走 AES-GCM（微秒级）；
// 系统级保护只在 New 时用一次。
type Store struct {
	db *sql.DB
	// dataDir 用于非 Windows 平台的密钥文件；Windows 下不用。
	dataDir string

	mu  sync.RWMutex
	dek []byte
	// protection 是当前数据密钥的保护方式，随 dek 一起刷新。
	protection Protection
}

// Options 构造 Store。
type Options struct {
	DB *sql.DB
	// DataDir 是密钥文件的落盘目录（非 Windows 平台使用）。
	DataDir string
}

// New 打开或初始化存储。
//
// 首次调用会生成数据密钥并用平台机制保护后写库；
// 之后调用会解保护、缓存在内存。
//
// 若库里已有数据密钥但解不开（换了用户/机器），返回 ErrKeyLost，
// 由上层提供重置入口——不能静默当作「没配密钥」。
func New(opts Options) (*Store, error) {
	if opts.DB == nil {
		return nil, fmt.Errorf("secret: db 不能为 nil")
	}
	s := &Store{db: opts.DB, dataDir: opts.DataDir}
	if err := s.loadOrCreate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// loadOrCreate 读取已有数据密钥；没有则生成新的。
func (s *Store) loadOrCreate(ctx context.Context) error {
	var wrapped []byte
	var prot string
	err := s.db.QueryRowContext(ctx,
		`SELECT wrapped_dek, protection FROM secret_meta WHERE id = ?`, dekID).
		Scan(&wrapped, &prot)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.create(ctx)
	case err != nil:
		return fmt.Errorf("读取数据密钥记录失败: %w", err)
	}

	dek, err := unprotectData(wrapped, s.dataDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrKeyLost, err)
	}
	if len(dek) != dekSize {
		return fmt.Errorf("%w: 解出的数据密钥长度异常", ErrKeyLost)
	}
	s.mu.Lock()
	s.dek = dek
	s.protection = Protection(prot)
	s.mu.Unlock()
	return nil
}

// create 生成新的数据密钥并落库。
func (s *Store) create(ctx context.Context) error {
	dek := make([]byte, dekSize)
	if _, err := rand.Read(dek); err != nil {
		return fmt.Errorf("生成数据密钥失败: %w", err)
	}
	wrapped, prot, err := protectData(dek, s.dataDir)
	if err != nil {
		return fmt.Errorf("保护数据密钥失败: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO secret_meta (id, wrapped_dek, protection, created_at) VALUES (?, ?, ?, ?)`,
		dekID, wrapped, string(prot), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("写入数据密钥失败: %w", err)
	}
	s.mu.Lock()
	s.dek = dek
	s.protection = prot
	s.mu.Unlock()
	return nil
}

// Protection 返回当前数据密钥的保护方式，供界面提示强度。
func (s *Store) Protection() Protection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.protection
}

// Put 加密并保存一个 Provider 的密钥。传空串等同于 Delete。
func (s *Store) Put(ctx context.Context, providerName, apiKey string) error {
	if providerName == "" {
		return fmt.Errorf("provider 名称不能为空")
	}
	if apiKey == "" {
		return s.Delete(ctx, providerName)
	}

	enc, err := s.encrypt([]byte(apiKey))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO provider_secrets (provider_name, key_encrypted, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(provider_name) DO UPDATE SET
		   key_encrypted = excluded.key_encrypted,
		   updated_at    = excluded.updated_at`,
		providerName, enc, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("保存 %s 的密钥失败: %w", providerName, err)
	}
	return nil
}

// Get 解密读取。未设置时返回 ("", false, nil)。
//
// 解不开时返回 ErrKeyLost：调用方应提示用户重置，而不是当成没配置。
func (s *Store) Get(ctx context.Context, providerName string) (string, bool, error) {
	var enc []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT key_encrypted FROM provider_secrets WHERE provider_name = ?`, providerName).
		Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取 %s 的密钥失败: %w", providerName, err)
	}

	plain, err := s.decrypt(enc)
	if err != nil {
		return "", false, fmt.Errorf("%s 的密钥解不开（数据密钥已更换？）: %w", providerName, err)
	}
	return string(plain), true, nil
}

// Has 报告是否已存有密钥，不解密、不返回明文。
func (s *Store) Has(ctx context.Context, providerName string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM provider_secrets WHERE provider_name = ?`, providerName).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("查询 %s 的密钥失败: %w", providerName, err)
	}
	return true, nil
}

// Delete 删除某个 Provider 的密钥。不存在时不是错误。
func (s *Store) Delete(ctx context.Context, providerName string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM provider_secrets WHERE provider_name = ?`, providerName)
	if err != nil {
		return fmt.Errorf("删除 %s 的密钥失败: %w", providerName, err)
	}
	return nil
}

// Reset 丢弃全部密钥并生成新的数据密钥。
//
// 用在「数据密钥解不开」之后：所有已存的 API Key 都会丢失，
// 用户需要重新填写。调用方必须做二次确认。
func (s *Store) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_secrets`); err != nil {
		return fmt.Errorf("清空密钥失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_meta`); err != nil {
		return fmt.Errorf("清空数据密钥失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交重置失败: %w", err)
	}

	// 旧的数据密钥与绑定文件一并作废，避免残留可用材料。
	s.mu.Lock()
	s.dek = nil
	s.protection = ""
	s.mu.Unlock()
	cleanupBinding(s.dataDir)

	if err := s.create(ctx); err != nil {
		return fmt.Errorf("重置后重新生成数据密钥失败: %w", err)
	}
	return nil
}

// encrypt 用当前数据密钥做 AES-256-GCM。输出格式：[12 字节 nonce][密文+tag]。
func (s *Store) encrypt(plain []byte) ([]byte, error) {
	s.mu.RLock()
	dek := s.dek
	s.mu.RUnlock()
	if len(dek) == 0 {
		return nil, ErrNoDEK
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("构造 AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM: %w", err)
	}

	out := make([]byte, 0, gcm.NonceSize()+len(plain)+gcm.Overhead())
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce: %w", err)
	}
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// decrypt 解开 encrypt 的输出。
func (s *Store) decrypt(enc []byte) ([]byte, error) {
	s.mu.RLock()
	dek := s.dek
	s.mu.RUnlock()
	if len(dek) == 0 {
		return nil, ErrNoDEK
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("构造 AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM: %w", err)
	}
	if len(enc) < gcm.NonceSize()+gcm.Overhead() {
		return nil, fmt.Errorf("密文长度异常")
	}
	nonce, body := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}
