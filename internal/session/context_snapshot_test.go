package session

import (
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
)

func snap(segs map[string]int) ContextSnapshot {
	out := ContextSnapshot{}
	for kind, tokens := range segs {
		out.Segments = append(out.Segments, contextx.Segment{
			Kind:   contextx.SegmentKind(kind),
			Tokens: tokens,
		})
	}
	return out
}

func TestDiffContextSnapshots(t *testing.T) {
	before := snap(map[string]int{
		"identity": 79, "tools": 378, "history": 500,
	})
	after := snap(map[string]int{
		"identity": 79, "tools": 420, "history": 800, "rag": 300,
	})

	got := DiffContextSnapshots(before, after)
	byKind := map[string]SegmentDiff{}
	for _, d := range got {
		byKind[d.Kind] = d
	}

	if d := byKind["identity"]; d.Status != "same" || d.Delta != 0 {
		t.Errorf("identity 应不变，得到 %+v", d)
	}
	if d := byKind["tools"]; d.Status != "changed" || d.Delta != 42 {
		t.Errorf("tools 应增加 42，得到 %+v", d)
	}
	if d := byKind["history"]; d.Status != "changed" || d.Delta != 300 {
		t.Errorf("history 应增加 300，得到 %+v", d)
	}
	if d := byKind["rag"]; d.Status != "added" || d.Before != 0 || d.After != 300 {
		t.Errorf("rag 应为新增，得到 %+v", d)
	}
}

func TestDiffContextSnapshotsRemoved(t *testing.T) {
	before := snap(map[string]int{"rag": 500, "identity": 79})
	after := snap(map[string]int{"identity": 79})

	got := DiffContextSnapshots(before, after)
	var rag SegmentDiff
	for _, d := range got {
		if d.Kind == "rag" {
			rag = d
		}
	}
	if rag.Status != "removed" || rag.Delta != -500 {
		t.Errorf("rag 应被移除，得到 %+v", rag)
	}
}

// 首次对比（无上一轮）时所有段都应标为 added。
func TestDiffContextSnapshotsFromEmpty(t *testing.T) {
	after := snap(map[string]int{"identity": 79, "history": 100})
	got := DiffContextSnapshots(ContextSnapshot{}, after)

	if len(got) != 2 {
		t.Fatalf("应有 2 段，得到 %d", len(got))
	}
	for _, d := range got {
		if d.Status != "added" {
			t.Errorf("%s 应为 added，得到 %s", d.Kind, d.Status)
		}
	}
}

// 输出顺序应稳定（按八段定义顺序），不受 map 迭代随机性影响。
func TestDiffContextSnapshotsStableOrder(t *testing.T) {
	segs := map[string]int{
		"identity": 1, "tools": 2, "profile": 3, "episodic": 4,
		"rag": 5, "history": 6, "input": 7, "notes": 8,
	}
	a := snap(segs)
	b := snap(segs)

	want := []string{"identity", "tools", "profile", "episodic", "rag", "history", "input", "notes"}
	for run := 0; run < 5; run++ {
		got := DiffContextSnapshots(a, b)
		if len(got) != len(want) {
			t.Fatalf("段数 = %d", len(got))
		}
		for i, kind := range want {
			if got[i].Kind != kind {
				t.Fatalf("第 %d 段 = %s，期望 %s（顺序不稳定）", i, got[i].Kind, kind)
			}
		}
	}
}

// 两次快照都为空时不应 panic。
func TestDiffContextSnapshotsEmpty(t *testing.T) {
	if got := DiffContextSnapshots(ContextSnapshot{}, ContextSnapshot{}); len(got) != 0 {
		t.Errorf("空对空应返回空，得到 %d 段", len(got))
	}
}
