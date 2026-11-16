package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

func newWriteTool(t *testing.T) (*FileWrite, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFileWrite(dir), dir
}

func run(t *testing.T, tl *FileWrite, args map[string]any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestFileWriteCreatesFile(t *testing.T) {
	tl, dir := newWriteTool(t)

	res := run(t, tl, map[string]any{"path": "notes/a.md", "content": "你好"})
	if res.IsError {
		t.Fatalf("写入失败: %v", res.Blocks)
	}

	got, err := os.ReadFile(filepath.Join(dir, "notes", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "你好" {
		t.Errorf("内容 = %q", got)
	}
}

func TestFileWriteAppend(t *testing.T) {
	tl, dir := newWriteTool(t)

	run(t, tl, map[string]any{"path": "log.txt", "content": "第一行\n"})
	run(t, tl, map[string]any{"path": "log.txt", "content": "第二行\n", "append": true})

	got, _ := os.ReadFile(filepath.Join(dir, "log.txt"))
	want := "第一行\n第二行\n"
	if string(got) != want {
		t.Errorf("内容 = %q，期望 %q", got, want)
	}
}

func TestFileWriteOverwritesByDefault(t *testing.T) {
	tl, dir := newWriteTool(t)

	run(t, tl, map[string]any{"path": "x.txt", "content": "旧"})
	run(t, tl, map[string]any{"path": "x.txt", "content": "新"})

	got, _ := os.ReadFile(filepath.Join(dir, "x.txt"))
	if string(got) != "新" {
		t.Errorf("默认应覆盖，内容 = %q", got)
	}
}

// 路径牢笼是硬约束：逃逸尝试一律拒绝，且不应在牢笼外留下文件。
func TestFileWriteRejectsEscape(t *testing.T) {
	tl, _ := newWriteTool(t)

	cases := []string{
		"../outside.txt",
		"../../outside.txt",
		"notes/../../outside.txt",
		"a/b/../../../../tmp/outside.txt",
	}

	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			res := run(t, tl, map[string]any{"path": p, "content": "x"})
			if !res.IsError {
				t.Errorf("路径 %q 应被拒绝", p)
			}
		})
	}
}

func TestFileWriteRejectsAbsoluteOutside(t *testing.T) {
	tl, dir := newWriteTool(t)

	outside := filepath.Join(filepath.Dir(dir), "escaped.txt")
	res := run(t, tl, map[string]any{"path": outside, "content": "x"})
	if !res.IsError {
		t.Errorf("牢笼外的绝对路径应被拒绝")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("逃逸路径不应留下文件")
	}
}

// 容器目录名以牢笼名为前缀时不能被误判为子路径。
func TestFileWriteRejectsSiblingPrefixDir(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(parent, "work-other")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	tl := NewFileWrite(root)
	res := run(t, tl, map[string]any{
		"path":    filepath.Join(sibling, "x.txt"),
		"content": "x",
	})
	if !res.IsError {
		t.Error("同级的近似目录名不应被当作牢笼内路径")
	}
}

func TestFileWriteEmptyPathRejected(t *testing.T) {
	tl, _ := newWriteTool(t)
	res := run(t, tl, map[string]any{"path": "  ", "content": "x"})
	if !res.IsError {
		t.Error("空路径应被拒绝")
	}
}

func TestFileWriteIsWriteRisk(t *testing.T) {
	tl, _ := newWriteTool(t)
	if tl.Spec().Risk != tool.RiskWrite {
		t.Errorf("风险等级 = %v，期望 write", tl.Spec().Risk)
	}
}

func TestFileWriteAllowsNestedUnderRoot(t *testing.T) {
	tl, dir := newWriteTool(t)
	res := run(t, tl, map[string]any{"path": "a/b/c/d.txt", "content": "深"})
	if res.IsError {
		t.Fatalf("嵌套路径应允许: %v", res.Blocks)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b", "c", "d.txt")); err != nil {
		t.Error("嵌套文件未创建")
	}
}

func TestFileWriteSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// 在牢笼内建一个指向外面的符号链接
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("当前平台不支持创建符号链接")
	}

	tl := NewFileWrite(root)
	res := run(t, tl, map[string]any{"path": "escape/x.txt", "content": "x"})
	if !res.IsError {
		t.Error("经符号链接逃逸应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Error("不应在牢笼外写入文件")
	}
}
