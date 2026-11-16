package web

// 技能启停的端到端断言：禁用后不仅要落盘，还要从模型看到的
// 上下文里消失。只测接口返回值等于没测——M1 就发生过
// 「SkillLines 在 session 填了、agent 声明了，但构造上下文时忘了转发」。

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
	"github.com/lincanyitse/baiyi-chat/internal/session"
	"github.com/lincanyitse/baiyi-chat/internal/skill"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

func writeSkillDoc(t *testing.T, root, dir, name, desc string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文\n"
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func digestLines(c *skill.Catalog) []string {
	ds := c.Digests()
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name+"："+d.Description)
	}
	return out
}

// TestSkillDisableRemovesFromContext 是本次的核心验收。
func TestSkillDisableRemovesFromContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkillDoc(t, root, "alpha", "alpha", "阿尔法技能")

	cat := skill.NewCatalog(root)
	if _, err := cat.Refresh(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := session.NewService(session.Options{
		Log:        logger,
		System:     "测试",
		Tools:      []tool.Tool{skill.NewLoadTool(cat)},
		Skills:     cat,
		SkillLines: digestLines(cat),
	})

	srv := New(Options{
		Config:       config.WebConfig{Listen: "127.0.0.1:0"},
		Service:      svc,
		Log:          logger,
		Skills:       cat,
		ReloadSkills: svc.ReloadSkills,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	before := strings.Join(svc.SkillLines(), "\n")
	if !strings.Contains(before, "alpha") {
		t.Fatalf("前置条件不成立：禁用前上下文里应有 alpha，实际 %q", before)
	}

	resp, raw := plainReq(t, "PATCH", ts.URL+"/api/skills/alpha", map[string]bool{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("禁用失败: %d %s", resp.StatusCode, raw)
	}

	after := strings.Join(svc.SkillLines(), "\n")
	if strings.Contains(after, "alpha") {
		t.Fatalf("禁用后上下文里仍有 alpha: %q", after)
	}
}
