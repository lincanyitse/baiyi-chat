package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveMCPDoesNotDropProviders 是这次发现的静默数据丢失的回归测试。
//
// replaceSections 早先按「本包管理的全部段」删除，导致保存 MCP 时
// 把 [providers.*] 与 [route.*] 一起删了。配置看起来保存成功，
// 用户重启后模型全没了。
func TestSaveMCPDoesNotDropProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := `# 用户手写的说明，不该被删
[providers.p]
kind        = "openai-compat"
base_url    = "http://x/v1"
api_key_env = "MY_KEY"

[providers.p.models.m]
context_window = 1000
tools          = true

[route.chat]
candidates = ["p/m"]
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := SaveMCP(path, []MCPServer{
		{Alias: "fs", Transport: "stdio", Command: "npx", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	for _, want := range []string{
		"[providers.p]", "base_url", "[providers.p.models.m]",
		"[route.chat]", "candidates", "[[mcp]]", "alias",
		"# 用户手写的说明，不该被删",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("写回后丢了 %q:\n%s", want, got)
		}
	}

	// 重新加载确认结构完整
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("写回后应能加载: %v", err)
	}
	if _, ok := cfg.Providers["p"]; !ok {
		t.Error("provider 丢了")
	}
	if len(cfg.MCP) != 1 || cfg.MCP[0].Alias != "fs" {
		t.Errorf("MCP 段不对: %+v", cfg.MCP)
	}
}

// TestSaveMCPIsIdempotent 连续保存同样的配置，文件内容不该变。
//
// 早先每次保存都会在文件头累积一个空行，三次之后配置变成一片空白开头。
func TestSaveMCPIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	servers := []MCPServer{
		{Alias: "a", Transport: "http", URL: "http://x", Enabled: true},
		{Alias: "b", Transport: "stdio", Command: "npx", Enabled: false},
	}

	if _, err := SaveMCP(path, servers); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := SaveMCP(path, servers); err != nil {
			t.Fatal(err)
		}
	}
	last, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(last) {
		t.Fatalf("重复保存不该改变文件内容。\n第一次:\n%s\n第三次:\n%s", first, last)
	}
	if strings.HasPrefix(string(last), "\n") {
		t.Errorf("文件不该以空行开头:\n%q", string(last))
	}
}

// TestSaveProvidersDoesNotDropMCP 反向验证：保存 Provider 不该删掉 [[mcp]]。
func TestSaveProvidersDoesNotDropMCP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := `[providers.old]
kind     = "anthropic"
base_url = "http://old"

[[mcp]]
alias     = "keepme"
transport = "http"
url       = "http://127.0.0.1:3000/mcp"
enabled   = true
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := SaveProviders(path, map[string]Provider{
		"new": {Kind: "anthropic", BaseURL: "http://new"},
	}, map[string]Route{})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	if !strings.Contains(got, "[[mcp]]") || !strings.Contains(got, "keepme") {
		t.Errorf("写回 Provider 时丢了 MCP 段:\n%s", got)
	}
	if strings.Contains(got, "[providers.old]") {
		t.Errorf("旧的 provider 段应该被替换:\n%s", got)
	}
}
