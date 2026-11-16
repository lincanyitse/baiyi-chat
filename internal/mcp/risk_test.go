package mcp

import (
	"log/slog"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// 风险级别的映射是安全边界：填错不能让写操作变成「自动执行」。
func TestRiskMapping(t *testing.T) {
	cases := []struct {
		in   string
		want tool.RiskLevel
	}{
		{"", tool.RiskRead},
		{"read", tool.RiskRead},
		{"write", tool.RiskWrite},
		{"dangerous", tool.RiskDangerous},
		// 未知值一律按最严格处理：用户填错时宁可多要一次审批，
		// 也不能悄悄降级成免审批。
		{"READ", tool.RiskDangerous},
		{"rw", tool.RiskDangerous},
		{"typo", tool.RiskDangerous},
	}

	for _, tc := range cases {
		c := NewClient(ServerConfig{Alias: "x", Risk: tc.in}, slog.Default())
		if got := c.risk(); got != tc.want {
			t.Errorf("Risk=%q 应映射为 %v，实际 %v", tc.in, tc.want, got)
		}
	}
}
