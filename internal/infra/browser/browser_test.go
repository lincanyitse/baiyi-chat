package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldOpen(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "last-url")

	// 没有状态文件 = 第一次
	if !shouldOpen(state, "http://127.0.0.1:8787") {
		t.Fatal("首次启动应该打开浏览器")
	}

	remember(state, "http://127.0.0.1:8787")

	// 同一个地址：不重复打开
	if shouldOpen(state, "http://127.0.0.1:8787") {
		t.Fatal("地址未变时不该重复打开")
	}

	// 端口变了：旧页已经连不上，必须开新的
	if !shouldOpen(state, "http://127.0.0.1:9000") {
		t.Fatal("地址变化时应该打开新页")
	}
}

func TestRememberRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "last-url")

	remember(state, "http://127.0.0.1:8787")

	got, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "http://127.0.0.1:8787" {
		t.Fatalf("状态文件内容不对: %q", got)
	}
}
