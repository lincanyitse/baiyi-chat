package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// captureSink 记录每次组装的完整输入，用于断言接线。
type captureSink struct {
	mu   sync.Mutex
	outs []*contextx.Output
}

func (c *captureSink) SaveSnapshot(_ context.Context, _ string, _ int, _ string, out *contextx.Output) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *out
	c.outs = append(c.outs, &cp)
	return nil
}

func (c *captureSink) last(t *testing.T) *contextx.Output {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.outs) == 0 {
		t.Fatal("没有产生快照")
	}
	return c.outs[len(c.outs)-1]
}

// TestTurnInputReachesAssembler 是「三段接线」测试。
//
// 它捕获的是接线断链——M1 就发生过 SkillLines 在 session 填了、
// agent 声明了，但构造 contextx.Input 时忘了转发的 bug。
// 断言内存对象没有意义：必须断言最终到达组装器的数据。
func TestTurnInputReachesAssembler(t *testing.T) {
	sink := &captureSink{}
	engine := New(Options{Store: newFakeStore(), Log: discardLogger()})

	res, err := engine.Run(context.Background(), TurnInput{
		TurnID:    "t1",
		SessionID: "s1",
		UserText:  "问题",
		Provider:  &fakeProvider{scripts: [][]llm.Event{{llm.TextDeltaEvent{Text: "答"}}}},
		Model:     "m",
		System:    "身份",
		SkillLines: []string{
			"deploy：部署技能",
		},
		ProfileCard: "用户偏好：简洁",
		Episodes:    []string{"上次谈到容器部署"},
		RAGChunks: []contextx.RAGChunk{
			{ID: "c1", Title: "手册", Text: "容器部署步骤", Score: 0.9},
		},
		Notes:     "便签内容",
		Snapshots: sink,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res

	out := sink.last(t)

	// 断言每一段都真的进了组装结果
	checks := []struct {
		name string
		kind contextx.SegmentKind
		want string
	}{
		{"技能目录", contextx.SegTools, "deploy"},
		{"画像卡", contextx.SegProfile, "简洁"},
		{"情节召回", contextx.SegEpisodic, "容器部署"},
		{"RAG", contextx.SegRAG, "容器部署步骤"},
		{"便签", contextx.SegNotes, "便签内容"},
	}
	for _, c := range checks {
		seg := findSegment(out.Segments, c.kind)
		if seg == nil {
			t.Fatalf("%s 段缺失", c.name)
		}
		if !containsStr(seg.Text, c.want) {
			t.Fatalf("%s 段内容错误：期望包含 %q，实际 %q", c.name, c.want, seg.Text)
		}
	}
}

// TestVolatileSegmentsDoNotEnterSystem 验证 M2 的核心约束：
// 段 4/5/8 变化不能让 system 前缀变化，否则 Anthropic 缓存每轮失效。
func TestVolatileSegmentsDoNotEnterSystem(t *testing.T) {
	run := func(episodes []string, rag []contextx.RAGChunk, notes string) (string, string) {
		sink := &captureSink{}
		engine := New(Options{Store: newFakeStore(), Log: discardLogger()})
		_, err := engine.Run(context.Background(), TurnInput{
			TurnID: "t", SessionID: "s", UserText: "问题",
			Provider:    &fakeProvider{scripts: [][]llm.Event{{llm.TextDeltaEvent{Text: "答"}}}},
			Model:       "m",
			System:      "身份",
			SkillLines:  []string{"s1：技能"},
			ProfileCard: "画像",
			Episodes:    episodes,
			RAGChunks:   rag,
			Notes:       notes,
			Snapshots:   sink,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := sink.last(t)
		return out.System, out.PrefixHash
	}

	sys1, hash1 := run([]string{"第一轮的情节"}, nil, "")
	sys2, hash2 := run([]string{"完全不同的一轮"}, []contextx.RAGChunk{{ID: "x", Title: "t", Text: "别的资料"}}, "新便签")

	if sys1 != sys2 {
		t.Fatalf("易变段不该影响 system：\n%q\n%q", sys1, sys2)
	}
	if hash1 != hash2 {
		t.Fatalf("易变段变化不该改变 PrefixHash：%s → %s", hash1, hash2)
	}
	if containsStr(sys1, "第一轮的情节") || containsStr(sys1, "别的资料") {
		t.Fatalf("易变内容混进了 system：%q", sys1)
	}
}

func findSegment(segs []contextx.Segment, kind contextx.SegmentKind) *contextx.Segment {
	for i := range segs {
		if segs[i].Kind == kind {
			return &segs[i]
		}
	}
	return nil
}

func containsStr(hay, needle string) bool {
	return len(needle) == 0 || indexOfStr(hay, needle) >= 0
}

func indexOfStr(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
