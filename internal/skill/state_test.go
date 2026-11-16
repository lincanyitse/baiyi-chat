package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// skillDoc 生成一份最小的 SKILL.md 内容。
func skillDoc(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文\n"
}

// TestSetEnabledPersistsAcrossCatalogs 验证禁用状态在「重启」后仍在。
func TestSetEnabledPersistsAcrossCatalogs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, root, "alpha", skillDoc("alpha", "第一个"))
	writeSkill(t, root, "beta", skillDoc("beta", "第二个"))

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}

	// 模拟重启：新建一个 Catalog，读同一个状态文件
	c2 := NewCatalog(root)
	if _, err := c2.Refresh(); err != nil {
		t.Fatal(err)
	}
	if c2.IsEnabled("alpha") {
		t.Fatal("禁用状态没有持久化")
	}
	if !c2.IsEnabled("beta") {
		t.Fatal("没有禁用 beta，它应该仍是启用的")
	}
}

// TestDisabledSkillExcludedFromAll 确认禁用的技能不会下发给模型。
func TestDisabledSkillExcludedFromAll(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, root, "alpha", skillDoc("alpha", "第一个"))

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}

	if len(c.All()) != 0 {
		t.Error("禁用后 All() 不该再返回它")
	}
	// 管理界面仍要看到
	if len(c.AllForAdmin()) != 1 {
		t.Error("AllForAdmin() 应包含被禁用的技能")
	}
}

// TestSetEnabledUnknownSkill 未知技能要报错，不能静默记录一个不存在的名字。
func TestSetEnabledUnknownSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("nope", false); err == nil {
		t.Fatal("未知技能应报错")
	}
}

// TestReEnableClearsPersistence 重新启用后状态文件里不该再有它。
func TestReEnableClearsPersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, root, "alpha", skillDoc("alpha", "第一个"))

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("alpha", true); err != nil {
		t.Fatal(err)
	}

	c2 := NewCatalog(root)
	if _, err := c2.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !c2.IsEnabled("alpha") {
		t.Fatal("重新启用后不该仍是禁用")
	}
}

// TestStateFileCorruptFallsBackToAllEnabled 状态文件损坏时不能丢技能。
func TestStateFileCorruptFallsBackToAllEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, root, "alpha", skillDoc("alpha", "第一个"))

	// 写一个坏文件
	if err := os.WriteFile(DefaultStatePath(root), []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !c.IsEnabled("alpha") {
		t.Fatal("状态文件损坏时应退回「全部启用」，而不是禁用一切")
	}
	if len(c.All()) != 1 {
		t.Fatal("技能不该因为状态文件损坏而消失")
	}
}

// TestStateFileOutsideSkillsDir 状态文件不该落在技能目录里——
// 用户分享 skills/ 时不该把「我关掉了哪些」一起带出去。
func TestStateFileOutsideSkillsDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, root, "alpha", skillDoc("alpha", "第一个"))

	c := NewCatalog(root)
	if _, err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("alpha", false); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "skills.json")); err == nil {
		t.Fatal("skills.json 不该落在技能目录内")
	}
	if _, err := os.Stat(DefaultStatePath(root)); err != nil {
		t.Fatalf("状态文件应存在于技能目录的父级: %v", err)
	}
}
