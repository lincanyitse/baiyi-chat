package mcp

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// ShortName 生成确定性短名，满足 ^[a-zA-Z0-9_-]{1,64}$。
//
// 用 FNV-32a 哈希前缀拼接，保证：
//   - 同一个 (alias, tool) 永远得到同一个名字，重启后仍能反查
//   - 不同 (alias, tool) 冲突概率极低
func ShortName(alias, toolName string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(alias + "\x00" + toolName))
	sum := h.Sum32()

	prefix := sanitizePart(alias)
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	name := fmt.Sprintf("mcp_%s_%08x", prefix, sum)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) &&
		strings.EqualFold(s[len(s)-len(suffix):], suffix)
}
