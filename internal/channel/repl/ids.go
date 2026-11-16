package repl

import "strings"

// matchesID 判断用户输入的短 ID 是否指向某个完整 ID。
//
// 三处命令（/switch、/kb rm、/memory rm|why）共用同一套规则：
// 精确匹配、短 ID 匹配、或完整 ID 的后缀匹配（用户常只看得到尾部）。
//
// 早先每个命令各写一份，规则已经分叉——同一个短 ID 在 /switch 能用、
// 在 /kb rm 却找不到。统一到一处，行为才可预期。
func matchesID(full, query string) bool {
	if query == "" {
		return false
	}
	return full == query ||
		shortID(full) == query ||
		strings.HasSuffix(full, query)
}
