//go:build !windows

package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const bindingFileName = "secret.key"

// 文件格式：[16 字节 salt][12 字节 nonce][AES-GCM 密文 + tag]
const (
	bindingSaltSize = 16
	bindingFileMode = 0o600
)

// bindingPath 返回密钥文件路径。
func bindingPath(dataDir string) string {
	return filepath.Join(dataDir, bindingFileName)
}

// protectData 用文件绑定方式保护数据密钥。
//
// 保护密钥由文件自身的身份（设备号 + inode）派生，而不是内容或路径：
// 文件被复制到别处（inode 变了）就解不开，同设备内移动（inode 不变）
// 仍然可用。这是「防止文件被搬走」，不是系统级加密——同一台机器上
// 有权限读 inode 的进程仍能重建密钥。界面上必须如实说明。
func protectData(plain []byte, dataDir string) ([]byte, Protection, error) {
	if dataDir == "" {
		return nil, "", fmt.Errorf("非 Windows 平台需要数据目录来放置密钥文件")
	}
	path := bindingPath(dataDir)

	salt := make([]byte, bindingSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, "", fmt.Errorf("生成 salt: %w", err)
	}

	// 先以空内容创建文件，拿到 inode 后再写密文：
	// 标题里的 inode 必须在写入之前确定并保持不变。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, bindingFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// 残留的空文件（上次写到一半崩溃）：重置场景下可以直接复用，
			// 因为调用方已经决定丢弃旧密钥。
			if err := os.Remove(path); err != nil {
				return nil, "", fmt.Errorf("清理残留密钥文件: %w", err)
			}
			return protectData(plain, dataDir)
		}
		return nil, "", fmt.Errorf("创建密钥文件: %w", err)
	}
	defer f.Close()

	key, err := bindingKey(path, salt)
	if err != nil {
		return nil, "", err
	}
	wrapped, err := seal(key, plain)
	if err != nil {
		return nil, "", err
	}

	// salt 是明文头部，不是秘密——它只用来让对方无法预计算派生。
	out := append(append([]byte(nil), salt...), wrapped...)
	if _, err := f.Write(out); err != nil {
		return nil, "", fmt.Errorf("写入密钥文件: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, "", fmt.Errorf("刷盘密钥文件: %w", err)
	}
	return out, ProtectionFileBinding, nil
}

// unprotectData 用文件当前的身份派生保护密钥并解开。
func unprotectData(wrapped []byte, dataDir string) ([]byte, error) {
	if len(wrapped) < bindingSaltSize+12+16 {
		return nil, fmt.Errorf("文件绑定密文长度异常")
	}
	path := bindingPath(dataDir)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("密钥文件不存在或不可读: %w", err)
	}

	salt := wrapped[:bindingSaltSize]
	key, err := bindingKey(path, salt)
	if err != nil {
		return nil, err
	}
	return open(key, wrapped[bindingSaltSize:])
}

// bindingKey 从文件身份派生 32 字节保护密钥。
//
// 用 HKDF 而不是直接哈希：派生结果在密码学意义上与输入不可区分，
// 且 salt 让同一个 inode 在不同文件里得到不同密钥。
func bindingKey(path string, salt []byte) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取密钥文件状态: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("当前平台不支持 inode 绑定")
	}
	binding := fmt.Sprintf("%d:%d", st.Dev, st.Ino)

	key, err := hkdf.Key(sha256.New, []byte(binding), salt, "baiyi/secret-dek/v1", 32)
	if err != nil {
		return nil, fmt.Errorf("派生保护密钥: %w", err)
	}
	return key, nil
}

func seal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func open(key, enc []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(enc) < gcm.NonceSize()+gcm.Overhead() {
		return nil, fmt.Errorf("密文长度异常")
	}
	nonce, body := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

// cleanupBinding 删除密钥文件。重置数据密钥时调用。
func cleanupBinding(dataDir string) {
	if dataDir == "" {
		return
	}
	_ = os.Remove(bindingPath(dataDir))
}
