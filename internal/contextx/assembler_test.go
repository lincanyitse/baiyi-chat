package contextx

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

func baseCaps(window int) llm.Capabilities {
	return llm.Capabilities{ContextWindow: window, MaxOutput: 1024, PrefixCache: true, MinCacheTokens: 100}
}

func smallInput() Input {
	return Input{
		Model:       "m",
		Caps:        baseCaps(200000),
		Identity:    "你是白泽。",
		ToolSpecs:   []llm.ToolSpec{{Name: "time_now", Description: "报时", InputSchema: json.RawMessage(`{}`)}},
		ProfileCard: "用户住在杭州\n喜欢简洁回答",
		History: []llm.Message{
			llm.NewText(llm.RoleUser, "早前的问题"),
			llm.NewText(llm.RoleAssistant, "早前的回答"),
		},
		UserInput: llm.NewText(llm.RoleUser, "当前问题"),
	}
}

func TestAssembleProducesEightSegments(t *testing.T) {
	out := New().Assemble(smallInput())

	if len(out.Segments) != len(order) {
		t.Fatalf("分段数 = %d, 期望 %d", len(out.Segments), len(order))
	}
	for i, seg := range out.Segments {
		if seg.Kind != order[i] {
			t.Errorf("段 %d = %s, 期望 %s", i, seg.Kind, order[i])
		}
		if seg.Order != i {
			t.Errorf("段 %d Order = %d", i, seg.Order)
		}
	}
}

func TestFirstThreeSegmentsFrozen(t *testing.T) {
	out := New().Assemble(smallInput())
	for i := 0; i < 3; i++ {
		if !out.Segments[i].Frozen {
			t.Errorf("第 %d 段应标记为 Frozen", i+1)
		}
	}
	for i := 3; i < len(out.Segments); i++ {
		if out.Segments[i].Frozen {
			t.Errorf("第 %d 段不应标记为 Frozen", i+1)
		}
	}
}

func TestStablePrefixAcrossTurns(t *testing.T) {
	// 同一份前 3 段，不同的问题，前缀哈希应相同
	a := smallInput()
	b := smallInput()
	b.UserInput = llm.NewText(llm.RoleUser, "完全不同的问题")

	outA := New().Assemble(a)
	outB := New().Assemble(b)

	if outA.PrefixHash != outB.PrefixHash {
		t.Errorf("前 3 段未变时前缀哈希应相同: %s vs %s", outA.PrefixHash, outB.PrefixHash)
	}
}

func TestPrefixChangesWhenIdentityChanges(t *testing.T) {
	a := smallInput()
	b := smallInput()
	b.Identity = "你是另一个助理。"

	if New().Assemble(a).PrefixHash == New().Assemble(b).PrefixHash {
		t.Error("身份变化时前缀哈希应变化")
	}
}

func TestCurrentInputNeverTrimmed(t *testing.T) {
	// 构造一个远超窗口的输入
	huge := strings.Repeat("很长的用户输入内容", 5000)
	in := smallInput()
	in.Caps = baseCaps(2000) // 极小窗口
	in.UserInput = llm.NewText(llm.RoleUser, huge)
	in.History = make([]llm.Message, 100)
	for i := range in.History {
		in.History[i] = llm.NewText(llm.RoleUser, strings.Repeat("历史内容", 50))
	}

	out := New().Assemble(in)

	var inputSeg *Segment
	for i := range out.Segments {
		if out.Segments[i].Kind == SegInput {
			inputSeg = &out.Segments[i]
		}
	}
	if inputSeg == nil {
		t.Fatal("找不到输入段")
	}
	if len(inputSeg.Trims) != 0 {
		t.Error("当前输入不应被裁剪")
	}
	if !strings.Contains(inputSeg.Text, huge[:100]) && len(inputSeg.Messages) == 0 {
		t.Error("当前输入内容应完整保留")
	}
}

func TestToolSchemaWholeOrNothing(t *testing.T) {
	// 工具段要么完整保留，要么整段为空，不允许截半
	in := smallInput()
	for i := 0; i < 50; i++ {
		in.ToolSpecs = append(in.ToolSpecs, llm.ToolSpec{
			Name: fmt.Sprintf("tool_%d", i), Description: "描述", InputSchema: json.RawMessage(`{}`),
		})
	}
	in.Caps = baseCaps(3000)

	out := New().Assemble(in)
	for _, seg := range out.Segments {
		if seg.Kind != SegTools {
			continue
		}
		// 若保留了内容，必须包含所有工具名
		if seg.Text == "" {
			continue
		}
		for i := 0; i < 50; i++ {
			name := fmt.Sprintf("tool_%d", i)
			if !strings.Contains(seg.Text, name) {
				t.Errorf("工具段被截断，缺少 %s", name)
			}
		}
	}
}

func TestDegradeDropsLowestRAGRAGFirst(t *testing.T) {
	in := smallInput()
	in.Caps = baseCaps(3000)
	in.RAGChunks = []RAGChunk{
		{ID: "high", Title: "高相关", Text: strings.Repeat("高相关内容", 100), Score: 0.9},
		{ID: "mid", Title: "中相关", Text: strings.Repeat("中相关内容", 100), Score: 0.5},
		{ID: "low", Title: "低相关", Text: strings.Repeat("低相关内容", 100), Score: 0.1},
	}

	out := New().Assemble(in)

	var ragSeg *Segment
	for i := range out.Segments {
		if out.Segments[i].Kind == SegRAG {
			ragSeg = &out.Segments[i]
		}
	}
	if ragSeg == nil {
		t.Fatal("找不到 RAG 段")
	}

	// 若有裁剪，必须先裁低分
	if len(ragSeg.Trims) > 0 {
		if ragSeg.Trims[0].ItemID != "low" {
			t.Errorf("首个被裁的应是低分片段，实际 %s", ragSeg.Trims[0].ItemID)
		}
	}
	// 高分片段应优先保留
	if strings.Contains(ragSeg.Text, "低相关内容") && !strings.Contains(ragSeg.Text, "高相关内容") {
		t.Error("高分片段应优先于低分片段保留")
	}
}

func TestDegradeStubsLargeToolResults(t *testing.T) {
	in := smallInput()
	in.Caps = baseCaps(4000)
	// 塞入一个巨大的工具结果
	in.History = append(in.History, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{
			llm.NewToolResultBlock("call_1", []llm.Block{
				llm.NewTextBlock(strings.Repeat("工具返回的大段内容", 400)),
			}, false),
		},
	})

	out := New().Assemble(in)

	var histSeg *Segment
	for i := range out.Segments {
		if out.Segments[i].Kind == SegHistory {
			histSeg = &out.Segments[i]
		}
	}
	if histSeg == nil {
		t.Fatal("找不到历史段")
	}

	var foundStub bool
	for _, tr := range histSeg.Trims {
		if tr.Reason == TrimStubToolResult {
			foundStub = true
		}
	}
	if !foundStub {
		t.Error("大工具结果应被折叠成桩")
	}
}

func TestDegradeOrderIsRespected(t *testing.T) {
	// 构造一个只会触发第一级降级（RAG）的场景：
	// 总量略超预算，但只靠丢弃低分 RAG 就能回到预算内。
	in := smallInput()
	in.Caps = baseCaps(4000)
	in.History = nil // 排除历史干扰
	in.RAGChunks = []RAGChunk{
		{ID: "high", Title: "A", Text: strings.Repeat("高相关内容", 400), Score: 0.9},
		{ID: "low", Title: "B", Text: strings.Repeat("低相关内容", 400), Score: 0.1},
	}

	out := New().Assemble(in)

	var ragTrims, profileTrims int
	for _, seg := range out.Segments {
		for _, tr := range seg.Trims {
			switch tr.Reason {
			case TrimDropRAG:
				ragTrims++
			case TrimShrinkProfile:
				profileTrims++
			}
		}
	}

	// 只丢 RAG 就够时，不应继续压画像卡——降级必须按需停止
	if ragTrims == 0 {
		t.Error("应触发 RAG 裁剪")
	}
	if profileTrims > 0 && ragTrims > 0 {
		// 允许两级都触发，但此时必须是因为仍超预算
		if out.TotalTokens <= out.Budget.Available {
			t.Errorf("已回到预算内却继续降级：rag=%d profile=%d", ragTrims, profileTrims)
		}
	}
}

func TestBudgetReport(t *testing.T) {
	out := New().Assemble(smallInput())

	if out.Budget.Available <= 0 {
		t.Error("应有可用预算")
	}
	if len(out.Budget.Allocated) != len(order) {
		t.Errorf("配额条目 = %d, 期望 %d", len(out.Budget.Allocated), len(order))
	}
	if out.Budget.TotalUsed != out.TotalTokens {
		t.Error("总用量与总 token 应一致")
	}
}

func TestSummaryBlockInjected(t *testing.T) {
	in := smallInput()
	in.Summary = "之前讨论了项目架构。"

	out := New().Assemble(in)

	var found bool
	for _, m := range out.Messages {
		if strings.Contains(llm.TextOf(m), "之前讨论了项目架构") {
			found = true
		}
	}
	if !found {
		t.Error("摘要应作为消息注入")
	}
}

func TestSystemContainsFrozenSegments(t *testing.T) {
	out := New().Assemble(smallInput())

	if !strings.Contains(out.System, "你是白泽") {
		t.Error("system 应包含身份")
	}
	if !strings.Contains(out.System, "time_now") {
		t.Error("system 应包含工具目录")
	}
	if strings.Contains(out.System, "当前问题") {
		t.Error("当前输入不应进入 system")
	}
}

func TestCacheBreakpointsOnlyWithPrefixCache(t *testing.T) {
	in := smallInput()
	in.Caps.PrefixCache = false
	if bp := New().Assemble(in).CacheBreakpoints; len(bp) != 0 {
		t.Errorf("不支持前缀缓存时不应有断点，实际 %v", bp)
	}

	in2 := smallInput()
	in2.Caps.PrefixCache = true
	in2.Caps.MinCacheTokens = 1
	if bp := New().Assemble(in2).CacheBreakpoints; len(bp) == 0 {
		t.Error("支持前缀缓存时应给出断点")
	}
}

func TestMessagesEndWithUserInput(t *testing.T) {
	out := New().Assemble(smallInput())
	if len(out.Messages) == 0 {
		t.Fatal("消息为空")
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != llm.RoleUser {
		t.Errorf("最后一条应是用户消息，实际 %s", last.Role)
	}
	if !strings.Contains(llm.TextOf(last), "当前问题") {
		t.Error("最后一条应包含当前输入")
	}
}

func TestEmptyInputsDoNotPanic(t *testing.T) {
	out := New().Assemble(Input{
		Caps:      baseCaps(10000),
		UserInput: llm.NewText(llm.RoleUser, "hi"),
	})
	if out.TotalTokens <= 0 {
		t.Error("至少应有输入的 token")
	}
}

func TestVolatileSegmentsDoNotPolluteSystem(t *testing.T) {
	// 段 4/5/8（情节、RAG、便签）逐轮变化时，
	// system 与 PrefixHash 都必须保持不变——否则 Anthropic 前缀缓存全废。
	base := smallInput()
	base.Episodes = []string{"上次讨论了项目架构"}
	base.RAGChunks = []RAGChunk{
		{ID: "c1", Title: "设计文档", Text: "这是一段参考资料", Score: 0.9},
	}
	base.Notes = "当前时间 10:00"

	changed := base
	changed.Episodes = []string{"完全不同的情节记忆"}
	changed.RAGChunks = []RAGChunk{
		{ID: "c2", Title: "另一份文档", Text: "不一样的参考资料", Score: 0.8},
	}
	changed.Notes = "当前时间 11:00"

	a := New().Assemble(base)
	b := New().Assemble(changed)

	if a.System != b.System {
		t.Errorf("易变段变化不应影响 system\nA: %q\nB: %q", a.System, b.System)
	}
	if a.PrefixHash != b.PrefixHash {
		t.Errorf("易变段变化不应影响前缀哈希: %s vs %s", a.PrefixHash, b.PrefixHash)
	}
}

func TestVolatileSegmentsInjectedIntoUserMessage(t *testing.T) {
	in := smallInput()
	in.RAGChunks = []RAGChunk{
		{ID: "c1", Title: "文档A", Text: "RAG 片段内容", Score: 0.9},
	}
	in.Notes = "便签内容"

	out := New().Assemble(in)

	// 易变内容应出现在最后一条用户消息里
	if len(out.Messages) == 0 {
		t.Fatal("无消息")
	}
	last := out.Messages[len(out.Messages)-1]
	text := llm.TextOf(last)
	if !strings.Contains(text, "RAG 片段内容") {
		t.Errorf("RAG 内容应进入当前用户消息: %q", text)
	}
	if !strings.Contains(text, "便签内容") {
		t.Errorf("便签应进入当前用户消息: %q", text)
	}
	if strings.Contains(out.System, "RAG 片段内容") {
		t.Error("RAG 内容不应进入 system")
	}
}

func TestStablePrefixHasNoVolatileContent(t *testing.T) {
	in := smallInput()
	in.Episodes = []string{"情节A"}
	in.RAGChunks = []RAGChunk{{ID: "c1", Title: "T", Text: "RAG正文", Score: 1}}
	in.Notes = "便签B"

	out := New().Assemble(in)

	for _, forbidden := range []string{"情节A", "RAG正文", "便签B"} {
		if strings.Contains(out.System, forbidden) {
			t.Errorf("system 不应包含易变内容 %q", forbidden)
		}
	}
	// 身份与工具目录必须还在
	if !strings.Contains(out.System, "你是白泽") {
		t.Error("system 应保留身份段")
	}
	if !strings.Contains(out.System, "time_now") {
		t.Error("system 应保留工具目录")
	}
}
