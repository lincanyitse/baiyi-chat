package attach

import (
	"os"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return NewStore(dir), dir
}

func TestSaveAndLoad(t *testing.T) {
	s, _ := newStore(t)
	data := []byte("hello attachment")

	id, err := s.Save(data, "text/plain", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("应返回附件 ID")
	}

	got, info, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("内容不匹配: %q", got)
	}
	if info.MediaType != "text/plain" {
		t.Errorf("media type = %q", info.MediaType)
	}
	if info.Name != "note.txt" {
		t.Errorf("name = %q", info.Name)
	}
	if info.Size != int64(len(data)) {
		t.Errorf("size = %d", info.Size)
	}
}

// 相同内容只落盘一份。
func TestSaveIsContentAddressed(t *testing.T) {
	s, dir := newStore(t)
	data := []byte("duplicate content")

	id1, err := s.Save(data, "text/plain", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Save(data, "text/plain", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("相同内容应得到相同 ID：%s vs %s", id1, id2)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 1 个内容文件 + 1 个 meta 文件
	if len(files) != 2 {
		t.Errorf("应只有 2 个文件，得到 %d: %v", len(files), names(files))
	}
}

func TestLoadMissing(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Load("abcdef0123456789"); err == nil {
		t.Error("不存在的附件应报错")
	}
}

// ID 只允许十六进制，挡住路径穿越。
func TestLoadRejectsBadID(t *testing.T) {
	s, _ := newStore(t)
	cases := []string{
		"",
		"../etc/passwd",
		"ABC",
		"zzzzzzzz",
		strings.Repeat("a", 100),
	}
	for _, c := range cases {
		if _, _, err := s.Load(c); err == nil {
			t.Errorf("ID %q 应被拒绝", c)
		}
	}
}

func TestDelete(t *testing.T) {
	s, _ := newStore(t)
	id, err := s.Save([]byte("to be deleted"), "text/plain", "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(id); err == nil {
		t.Error("删除后应读不到")
	}
}

func TestSaveEmptyData(t *testing.T) {
	s, _ := newStore(t)
	id, err := s.Save(nil, "application/octet-stream", "empty")
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("应为空，得到 %d 字节", len(data))
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}
