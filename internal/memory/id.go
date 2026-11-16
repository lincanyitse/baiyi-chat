package memory

import (
	"crypto/rand"
	"encoding/hex"
)

// newID 生成带前缀的短 ID。
//
// 用随机数而不是自增：记忆会被导出/导入到别的库，
// 自增 ID 在合并时必然冲突。
func newID(prefix string) string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源失败极罕见；退化为固定前缀仍保证唯一性由调用方重试
		return prefix + "_000000000000000000"
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
