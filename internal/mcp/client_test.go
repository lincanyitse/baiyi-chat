package mcp

import (
	"strings"
	"testing"
)

func TestExposedNameSimple(t *testing.T) {
	got := ExposedName("fs", "read_file")
	if got != "mcp__fs__read_file" {
		t.Errorf("ExposedName = %q", got)
	}
	if !isClean(got) {
		t.Error("生成的名字应满足字符集约束")
	}
	if len(got) > 64 {
		t.Error("生成的名字不应超过 64 字符")
	}
}

func TestExposedNameSanitizes(t *testing.T) {
	got := ExposedName("my server", "read.file")
	if strings.ContainsAny(got, " .") {
		t.Errorf("应替换非法字符，实际 %q", got)
	}
	if !isClean(got) {
		t.Errorf("清洗后应合法: %q", got)
	}
}

func TestExposedNameFallsBackToHashForLongNames(t *testing.T) {
	longTool := strings.Repeat("very_long_tool_name_", 5)
	got := ExposedName("server", longTool)

	if len(got) > 64 {
		t.Errorf("长名应退化为短名，实际长度 %d: %q", len(got), got)
	}
	if !strings.HasPrefix(got, "mcp_") {
		t.Errorf("短名应以 mcp_ 开头: %q", got)
	}
}

func TestShortNameDeterministic(t *testing.T) {
	a := ShortName("srv", "tool_name_with_many_characters_that_exceeds_limits")
	b := ShortName("srv", "tool_name_with_many_characters_that_exceeds_limits")
	if a != b {
		t.Errorf("同一输入应得到相同短名: %q vs %q", a, b)
	}

	c := ShortName("srv", "different_tool")
	if a == c {
		t.Error("不同工具应得到不同短名")
	}

	d := ShortName("other", "tool_name_with_many_characters_that_exceeds_limits")
	if a == d {
		t.Error("不同 server 的同名工具应区分")
	}
}

func TestShortNameLength(t *testing.T) {
	alias := strings.Repeat("a", 200)
	tool := strings.Repeat("t", 200)
	got := ShortName(alias, tool)
	if len(got) > 64 {
		t.Errorf("短名长度 = %d", len(got))
	}
	if !isClean(got) {
		t.Errorf("短名应合法: %q", got)
	}
}

func TestIsClean(t *testing.T) {
	valid := []string{"abc", "a_b-c", "ABC123", "a"}
	for _, s := range valid {
		if !isClean(s) {
			t.Errorf("%q 应合法", s)
		}
	}

	invalid := []string{"", "a b", "a.b", "中文", strings.Repeat("a", 65)}
	for _, s := range invalid {
		if isClean(s) {
			t.Errorf("%q 应非法", s)
		}
	}
}

func TestSanitizePart(t *testing.T) {
	cases := map[string]string{
		"read_file":  "read_file",
		"read.file":  "read_file",
		"read file":  "read_file",
		"with/slash": "with_slash",
		"中文":         "__",
	}
	for in, want := range cases {
		if got := sanitizePart(in); got != want {
			t.Errorf("sanitizePart(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestBuildCommandEmpty(t *testing.T) {
	if cmd := buildCommand(t.Context(), ServerConfig{}); cmd != nil {
		t.Error("没有 command 时应返回 nil")
	}
}

func TestBuildCommandSimple(t *testing.T) {
	cmd := buildCommand(t.Context(), ServerConfig{Command: "python", Args: []string{"server.py"}})
	if cmd == nil {
		t.Fatal("应构造出命令")
	}
	if cmd.Path == "" {
		t.Error("Path 不应为空")
	}
}

func TestClientCloseWithoutConnect(t *testing.T) {
	c := NewClient(ServerConfig{Alias: "x"}, nil)
	if err := c.Close(); err != nil {
		t.Errorf("未连接时 Close 不应报错: %v", err)
	}
}

func TestClientConnectRejectsUnknownTransport(t *testing.T) {
	c := NewClient(ServerConfig{Alias: "x", Transport: "carrier-pigeon"}, nil)
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("未知传输应报错")
	}
}

func TestClientConnectRequiresURLForHTTP(t *testing.T) {
	c := NewClient(ServerConfig{Alias: "x", Transport: "http"}, nil)
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("http 缺少 url 应报错")
	}
}
