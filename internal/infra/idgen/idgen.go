// Package idgen 生成 UUIDv7。时间有序，为将来同步留缝。
package idgen

import (
	"fmt"

	"github.com/google/uuid"
)

// New 返回一个新的 UUIDv7 字符串。
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		// 系统熵源失败是极端情况，退化为 v4 保证可用性。
		return uuid.NewString()
	}
	return id.String()
}

// MustNew 在生成失败时 panic，仅用于测试与初始化期。
func MustNew() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("idgen: %v", err))
	}
	return id.String()
}
