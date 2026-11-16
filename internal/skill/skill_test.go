package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validSkill = `---
name: meeting-notes
description: 把会议记录整理成结构化纪要与待办
version: 1.0.0
spec: 1
triggers:
  - 会议纪要
  - 整理会议
allowed-tools:
  - memory_search
---

# 流程

1. 提取决策
2. 提取待办
3. 输出结构化纪要
`

func TestLoadValid(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "meeting-notes", validSkill)

	s, err := Load(filepath.Join(root, "meeting-notes"))
	if err != nil {
		t.Fatal(err)
	}

	if s.Spec.Name != "meeting-notes" {
		t.Errorf("name = %q", s.Spec.Name)
	}
	if s.Spec.Description == "" {
		t.Error("description 不应为空")
	}
	if s.Spec.Version != "1.0.0" {
		t.Errorf("version = %q", s.Spec.Version)
	}
	if len(s.Spec.Triggers) != 2 {
		t.Errorf("triggers = %v", s.Spec.Triggers)
	}
	if !strings.Contains(s.Body, "提取决策") {
		t.Error("正文未正确提取")
	}
	if strings.Contains(s.Body, "name: meeting-notes") {
		t.Error("正文不应包含 frontmatter")
	}
}

func TestLoadMissingName(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", `---
description: 缺少名字
---
正文`)

	_, err := Load(filepath.Join(root, "bad"))
	if err == nil {
		t.Fatal("缺少 name 应报错")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("错误信息应指明缺 name: %v", err)
	}
}

func TestLoadMissingDescription(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", `---
name: x
---
正文`)

	if _, err := Load(filepath.Join(root, "bad")); err == nil {
		t.Fatal("缺少 description 应报错")
	}
}

func TestLoadNoFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "plain", "# 只有正文\n没有 frontmatter")

	_, err := Load(filepath.Join(root, "plain"))
	if err == nil {
		t.Fatal("缺少 frontmatter 应报错")
	}
}

func TestLoadUnclosedFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", "---\nname: x\ndescription: y\n")

	if _, err := Load(filepath.Join(root, "bad")); err == nil {
		t.Fatal("frontmatter 未闭合应报错")
	}
}

func TestCatalogScanAndDigest(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "a", `---
name: alpha
description: 第一项技能
---
正文 A`)
	writeSkill(t, root, "b", `---
name: beta
description: 第二项技能
---
正文 B`)

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}

	all := c.All()
	if len(all) != 2 {
		t.Fatalf("技能数 = %d", len(all))
	}
	// 应按名字排序
	if all[0].Spec.Name != "alpha" || all[1].Spec.Name != "beta" {
		t.Errorf("顺序 = %s, %s", all[0].Spec.Name, all[1].Spec.Name)
	}

	digests := c.Digests()
	if len(digests) != 2 || digests[0].Description == "" {
		t.Errorf("digests = %+v", digests)
	}
}

func TestCatalogSkipsBrokenSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "good", validSkill)
	writeSkill(t, root, "broken", "没有 frontmatter")

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}

	// 坏的不应拖垮好的
	if len(c.All()) != 1 {
		t.Errorf("应只加载 1 个技能，实际 %d", len(c.All()))
	}
}

func TestCatalogEmptyRoot(t *testing.T) {
	c := NewCatalog(filepath.Join(t.TempDir(), "nonexistent"))
	if _, err := c.Refresh(); err != nil {
		t.Fatalf("目录不存在不应报错: %v", err)
	}
	if len(c.All()) != 0 {
		t.Error("应为空")
	}
}

func TestCatalogHotReload(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "a", `---
name: alpha
description: 第一版
---
正文`)

	c := NewCatalog(root)
	changed, err := c.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("首次扫描应报告变化")
	}

	// 内容不变，不应重复加载
	changed, _ = c.Refresh()
	if changed {
		t.Error("内容未变时不应报告变化")
	}

	// 修改内容
	writeSkill(t, root, "a", `---
name: alpha
description: 第二版
---
新正文`)

	changed, _ = c.Refresh()
	if !changed {
		t.Error("内容变化后应重新加载")
	}
	s, _ := c.Get("alpha")
	if s.Spec.Description != "第二版" {
		t.Errorf("description = %q", s.Spec.Description)
	}
}

func TestMatchTriggers(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "mn", validSkill)

	c := NewCatalog(root)
	_, _ = c.Refresh()

	if got := c.MatchTriggers("帮我整理会议记录"); len(got) != 1 || got[0] != "meeting-notes" {
		t.Errorf("应命中 meeting-notes，实际 %v", got)
	}
	if got := c.MatchTriggers("今天天气如何"); len(got) != 0 {
		t.Errorf("不应命中任何技能，实际 %v", got)
	}
}

func TestSetEnabled(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "mn", validSkill)
	c := NewCatalog(root)
	_, _ = c.Refresh()

	if err := c.SetEnabled("meeting-notes", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("meeting-notes"); ok {
		t.Error("禁用后不应能取到")
	}
	if len(c.Digests()) != 0 {
		t.Error("禁用后不应出现在目录中")
	}

	_ = c.SetEnabled("meeting-notes", true)
	if _, ok := c.Get("meeting-notes"); !ok {
		t.Error("重新启用后应可取到")
	}
}

func TestLoadToolExecute(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "mn", validSkill)
	c := NewCatalog(root)
	_, _ = c.Refresh()

	lt := NewLoadTool(c)
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"name":"meeting-notes"}`), &tool.ExecContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("不应出错: %s", res.Text())
	}
	if !strings.Contains(res.Text(), "提取决策") {
		t.Error("应返回技能正文")
	}
}

func TestLoadToolUnknownSkill(t *testing.T) {
	c := NewCatalog(t.TempDir())
	_, _ = c.Refresh()

	lt := NewLoadTool(c)
	res, _ := lt.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`), &tool.ExecContext{})
	if !res.IsError {
		t.Fatal("不存在的技能应返回错误结果")
	}
	if !strings.Contains(res.Text(), "不存在") {
		t.Errorf("错误信息 = %s", res.Text())
	}
}

func TestLoadToolReference(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "mn", validSkill)

	refDir := filepath.Join(root, "mn", "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "format.md"), []byte("# 纪要模板\n\n- 决策\n- 待办"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewCatalog(root)
	_, _ = c.Refresh()

	lt := NewLoadTool(c)
	res, _ := lt.Execute(context.Background(),
		json.RawMessage(`{"name":"meeting-notes","reference":"references/format.md"}`),
		&tool.ExecContext{})
	if res.IsError {
		t.Fatalf("读取参考资料失败: %s", res.Text())
	}
	if !strings.Contains(res.Text(), "纪要模板") {
		t.Errorf("应返回参考资料内容: %s", res.Text())
	}
}

func TestReferenceRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "mn", validSkill)
	c := NewCatalog(root)
	_, _ = c.Refresh()
	s, _ := c.Get("meeting-notes")

	for _, bad := range []string{"../secret.md", "references/../../etc/passwd", "/etc/passwd"} {
		if _, err := s.Reference(bad); err == nil {
			t.Errorf("非法路径 %q 应被拒绝", bad)
		}
	}
}
