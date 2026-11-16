package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleProviders() (map[string]Provider, map[string]Route) {
	return map[string]Provider{
			"gw": {
				Kind:      "openai-compat",
				BaseURL:   "http://127.0.0.1:9000/v1",
				APIKeyEnv: "GW_KEY",
				TimeoutS:  60,
				Models: map[string]Model{
					"m1": {ContextWindow: 128000, MaxOutput: 8192, Tools: true},
				},
			},
		}, map[string]Route{
			"chat": {Candidates: []string{"gw/m1"}},
		}
}

// 关键约束：只改 Provider/Route，其他段必须逐字保留。
func TestSaveProvidersPreservesOtherSections(t *testing.T) {
	orig := `# 顶部注释
data_dir = "/tmp/x"

[log]
level = "debug"
format = "text"

[providers.old]
kind = "anthropic"
base_url = "http://old"

[route.chat]
candidates = ["old/x"]

[session]
max_steps = 7
`
	path := writeTemp(t, orig)
	providers, routes := sampleProviders()

	cfg, err := SaveProviders(path, providers, routes)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)

	// 未受管的段与注释必须原样活下来
	for _, want := range []string{
		"# 顶部注释",
		`data_dir = "/tmp/x"`,
		`[log]`,
		`level = "debug"`,
		`[session]`,
		`max_steps = 7`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("丢失了原有内容 %q\n---\n%s", want, text)
		}
	}

	// 旧的 provider 必须被替换掉，不能两份并存
	if strings.Contains(text, "[providers.old]") {
		t.Errorf("旧 provider 未被移除\n%s", text)
	}
	if !strings.Contains(text, "[providers.gw]") {
		t.Errorf("新 provider 未写入\n%s", text)
	}

	// 返回值应是重新加载后的生效配置
	if cfg.Providers["gw"].BaseURL != "http://127.0.0.1:9000/v1" {
		t.Errorf("返回值未反映新配置: %+v", cfg.Providers["gw"])
	}
	if cfg.Session.MaxSteps != 7 {
		t.Errorf("未受管配置丢了: MaxSteps = %d", cfg.Session.MaxSteps)
	}
}

// 原文件完全没有 provider 段时，应追加到末尾。
func TestSaveProvidersAppendsWhenMissing(t *testing.T) {
	path := writeTemp(t, "[log]\nlevel = \"info\"\n")
	providers, routes := sampleProviders()

	if _, err := SaveProviders(path, providers, routes); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "[providers.gw]") {
		t.Errorf("应追加 provider 段\n%s", got)
	}
	if !strings.Contains(string(got), `level = "info"`) {
		t.Errorf("原有 log 段丢失\n%s", got)
	}
}

// 同名 provider 的多个模型与 headers 都要能往返。
func TestSaveProvidersRoundTripModels(t *testing.T) {
	path := writeTemp(t, "")
	providers := map[string]Provider{
		"p": {
			Kind:      "anthropic",
			BaseURL:   "https://api.example.com",
			APIKeyEnv: "KEY",
			Headers:   map[string]string{"X-Trace": "1"},
			Models: map[string]Model{
				"a/b": {ContextWindow: 200000, MaxOutput: 4096, Tools: true, Vision: true, Thinking: true, PriceIn: 3.5, PriceOut: 15},
				"c":   {ContextWindow: 8000, Tools: false},
			},
		},
	}
	routes := map[string]Route{
		"chat":     {Candidates: []string{"p/a/b"}},
		"tool_use": {Candidates: []string{"p/c"}},
	}

	cfg, err := SaveProviders(path, providers, routes)
	if err != nil {
		t.Fatal(err)
	}

	got := cfg.Providers["p"]
	if got.Kind != "anthropic" || got.APIKeyEnv != "KEY" {
		t.Errorf("基本字段丢了: %+v", got)
	}
	if got.Headers["X-Trace"] != "1" {
		t.Errorf("headers 丢了: %+v", got.Headers)
	}
	if m := got.Models["a/b"]; m.ContextWindow != 200000 || !m.Vision || m.PriceIn != 3.5 {
		t.Errorf("模型 a/b 往返失败: %+v", m)
	}
	if m := got.Models["c"]; m.ContextWindow != 8000 || m.Tools {
		t.Errorf("模型 c 往返失败: %+v", m)
	}
	if len(cfg.Route["tool_use"].Candidates) != 1 {
		t.Errorf("路由往返失败: %+v", cfg.Route)
	}
}

// 特殊字符与需要引号的键名不能被写坏。
func TestSaveProvidersQuotesSpecialNames(t *testing.T) {
	path := writeTemp(t, "")
	providers := map[string]Provider{
		"my provider": {
			Kind:      "openai-compat",
			BaseURL:   `http://x/?a="b"`,
			APIKeyEnv: "KEY",
			Models: map[string]Model{
				"org/model-v2": {ContextWindow: 1000, Tools: true},
			},
		},
	}

	cfg, err := SaveProviders(path, providers, nil)
	if err != nil {
		t.Fatalf("含空格与引号的配置应能写回: %v", err)
	}
	if got := cfg.Providers["my provider"].BaseURL; got != `http://x/?a="b"` {
		t.Errorf("URL 转义失败: %q", got)
	}
	if _, ok := cfg.Providers["my provider"].Models["org/model-v2"]; !ok {
		t.Error("带斜杠的模型 ID 丢失")
	}
}

// 写回后的配置必须能被重新加载（语法自检在落盘前完成）。
func TestSaveProvidersOutputReloads(t *testing.T) {
	path := writeTemp(t, "[log]\nlevel = \"info\"\n")
	providers, routes := sampleProviders()

	if _, err := SaveProviders(path, providers, routes); err != nil {
		t.Fatalf("合法配置不应失败: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("写回后应能重新加载: %v", err)
	}
}

func TestReplaceSectionsHandlesNestedHeaders(t *testing.T) {
	orig := `[providers.a]
kind = "x"

[providers.a.models.m]
tools = true

[other]
k = 1
`
	got := replaceSections(orig, "[providers.b]\nkind = \"y\"\n", isProviderHeader)
	if strings.Contains(got, "[providers.a]") || strings.Contains(got, "[providers.a.models.m]") {
		t.Errorf("嵌套段未清干净:\n%s", got)
	}
	if !strings.Contains(got, "[other]") {
		t.Errorf("其他段被误删:\n%s", got)
	}
	if !strings.Contains(got, "[providers.b]") {
		t.Errorf("新段未插入:\n%s", got)
	}
}
