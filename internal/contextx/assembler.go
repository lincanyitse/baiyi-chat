package contextx

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// Assembler 把八段材料组装成一次推理请求的上下文。
type Assembler struct{}

// New 构造组装器。
func New() *Assembler { return &Assembler{} }

// Assemble 组装上下文。
//
// 流程：按固定顺序构造八段 → 超限时按降级链逐级裁剪 → 计算缓存断点。
func (a *Assembler) Assemble(in Input) Output {
	available := in.Caps.Usable()
	q := computeQuotas(available, in.BudgetOverride)

	plan := &assemblyPlan{in: in, quotas: q}
	plan.build()

	// 严格按序降级，不可跳步（架构纲要 §5.3）。
	//
	// 注意：停止条件不能只看「总量是否超预算」。单个弹性段（如 RAG）
	// 可能自身超出配额而总量仍有余量——配额分配的意义就是限制单段膨胀，
	// 所以每一级都要独立判断是否仍需处理。
	for _, step := range degradeChain {
		if plan.settled() {
			break
		}
		step(plan)
	}

	return plan.finish()
}

// assemblyPlan 是组装过程中的可变状态。
type assemblyPlan struct {
	in     Input
	quotas quotas
	segs   []Segment
	// history 是当前保留的历史消息（可被滚动摘要与丢弃替换）
	history []llm.Message
	// summary 是已生成的滚动摘要
	summary string
}

func (p *assemblyPlan) build() {
	p.segs = make([]Segment, 0, len(order))
	p.history = append([]llm.Message(nil), p.in.History...)
	p.summary = p.in.Summary

	for _, kind := range order {
		seg := Segment{Kind: kind, Order: segmentIndex(kind)}
		switch kind {
		case SegIdentity:
			seg.Text = p.in.Identity
			seg.Frozen = true
		case SegTools:
			seg.Text = renderTools(p.in.ToolSpecs, p.in.SkillLines)
			seg.Frozen = true
		case SegProfile:
			seg.Text = p.in.ProfileCard
			seg.Frozen = true
		case SegEpisodic:
			seg.Text = strings.Join(p.in.Episodes, "\n")
		case SegRAG:
			seg.Text = renderRAG(p.in.RAGChunks)
		case SegHistory:
			seg.Messages = p.history
			// 本轮工具往返也归入历史段展示——模型确实看到了它们，
			// 快照必须如实反映，否则「为什么这轮这么贵」永远解释不清。
			if len(p.in.LiveTurns) > 0 {
				seg.Messages = append(append([]llm.Message(nil), p.history...), p.in.LiveTurns...)
			}
			if p.summary != "" {
				seg.Text = summaryBlock(p.summary)
			}
		case SegInput:
			seg.Messages = []llm.Message{p.in.UserInput}
		case SegNotes:
			seg.Text = p.in.Notes
		}
		seg.Tokens = segmentTokens(seg)
		seg.Hash = segmentHash(seg)
		p.segs = append(p.segs, seg)
	}
}

// total 返回当前总 token。
func (p *assemblyPlan) total() int {
	total := 0
	for _, s := range p.segs {
		total += s.Tokens
	}
	return total
}

// withinBudget 报告总 token 是否在可用预算内。
func (p *assemblyPlan) withinBudget() bool {
	return p.total() <= p.in.Caps.Usable()
}

// overQuota 报告某段是否超出其分配配额。
func (p *assemblyPlan) overQuota(k SegmentKind) bool {
	seg := p.seg(k)
	if seg == nil {
		return false
	}
	limit := p.quotas.budgetFor(k)
	return limit > 0 && seg.Tokens > limit
}

// settled 报告是否所有该处理的问题都已解决。
//
// 降级链只需在「总量超预算」或「某弹性段超配额」时继续。
// 前 3 段是稳定前缀，不参与配额裁剪——它们超了只能精简文本，不是自动能做的。
func (p *assemblyPlan) settled() bool {
	if !p.withinBudget() {
		return false
	}
	for _, k := range []SegmentKind{SegRAG, SegProfile, SegEpisodic, SegNotes} {
		if p.overQuota(k) {
			return false
		}
	}
	return true
}

// seg 按类型取分段指针。
func (p *assemblyPlan) seg(k SegmentKind) *Segment {
	for i := range p.segs {
		if p.segs[i].Kind == k {
			return &p.segs[i]
		}
	}
	return nil
}

// degrade 是一级降级动作。
type degrade func(*assemblyPlan)

// degradeChain 是降级顺序。写死为切片而非可注册，
// 因为架构要求「严格按序不可跳步」——可配置化只会制造漂移。
var degradeChain = []degrade{
	dropLowestRAG,     // 1. 丢弃 RAG 中分数最低的片段
	shrinkProfileCard, // 2. 压缩画像卡
	stubToolResults,   // 3. 历史工具结果换句柄
	rollupHistory,     // 4. 滚动摘要压缩最老历史
	dropOldestTurns,   // 5. 丢弃最老轮次
	// 6. 仍超限则如实报告（在 finish 中处理），绝不静默截断
}

// dropLowestRAG 丢弃 RAG 中重排分数最低的片段。
//
// 触发条件有两类：总量超预算，或 RAG 段自身超出其配额。
// 后者是「预算分配」的意义所在——即使总量还有余量，
// 也不能让单个弹性段无限膨胀。
//
// 实现上预计算每个片段的 token 并增量累减，避免「丢一个就全量重渲染」
// 的 O(n²) 行为——RAG 段是最大弹性池，几十个片段很常见。
func dropLowestRAG(p *assemblyPlan) {
	seg := p.seg(SegRAG)
	if seg == nil || len(p.in.RAGChunks) == 0 {
		return
	}

	type scored struct {
		chunk  RAGChunk
		tokens int
	}
	items := make([]scored, len(p.in.RAGChunks))
	total := 0
	for i, c := range p.in.RAGChunks {
		t := llm.EstimateText(renderRAG([]RAGChunk{c}))
		items[i] = scored{chunk: c, tokens: t}
		total += t
	}
	// 高分在前，从尾部丢
	sort.Slice(items, func(i, j int) bool { return items[i].chunk.Score > items[j].chunk.Score })

	limit := p.quotas.RAG
	otherTokens := p.total() - seg.Tokens // 其余段占用的 token
	budget := p.in.Caps.Usable()

	keep := len(items)
	for keep > 0 {
		// 停止条件：RAG 回到配额内，且总量回到预算内
		if total <= limit && otherTokens+total <= budget {
			break
		}
		keep--
		dropped := items[keep]
		total -= dropped.tokens
		seg.Trims = append(seg.Trims, Trim{
			ItemID: dropped.chunk.ID, Tokens: dropped.tokens,
			Reason: TrimDropRAG, Ref: dropped.chunk.ID,
		})
	}

	kept := make([]RAGChunk, 0, keep)
	for i := 0; i < keep; i++ {
		kept = append(kept, items[i].chunk)
	}
	seg.Text = renderRAG(kept)
	seg.Tokens = total
	seg.Hash = hashText(seg.Text)
}

// shrinkProfileCard 把画像卡压到配额内（按行截断，保留高优先级条目）。
func shrinkProfileCard(p *assemblyPlan) {
	seg := p.seg(SegProfile)
	if seg == nil || seg.Tokens <= p.quotas.Profile {
		return
	}

	lines := strings.Split(seg.Text, "\n")
	kept := make([]string, 0, len(lines))
	used := 0
	for _, line := range lines {
		t := llm.EstimateText(line)
		if used+t > p.quotas.Profile {
			seg.Trims = append(seg.Trims, Trim{
				ItemID: line, Tokens: t, Reason: TrimShrinkProfile,
			})
			continue
		}
		kept = append(kept, line)
		used += t
	}
	seg.Text = strings.Join(kept, "\n")
	seg.Tokens = used
	seg.Hash = hashText(seg.Text)
}

// stubToolResults 把历史里的大块工具结果替换成句柄占位。
//
// 这是性价比最高的一级：工具结果通常是上下文里最大的膨胀源，
// 而模型多数时候不需要完整原文。
func stubToolResults(p *assemblyPlan) {
	const stubThreshold = 500 // token

	seg := p.seg(SegHistory)
	if seg == nil {
		return
	}

	changed := false
	for i, msg := range p.history {
		blocks := make([]llm.Block, 0, len(msg.Content))
		for _, blk := range msg.Content {
			tr, ok := blk.(llm.ToolResultBlock)
			if !ok {
				blocks = append(blocks, blk)
				continue
			}
			tokens := llm.EstimateBlocks(tr.Content)
			if tokens <= stubThreshold {
				blocks = append(blocks, blk)
				continue
			}
			// 已经是桩的跳过
			if tr.Truncated {
				blocks = append(blocks, blk)
				continue
			}
			stub := llm.ToolResultBlock{
				Type:      string(llm.BlockToolResult),
				ToolUseID: tr.ToolUseID,
				Truncated: true,
				Handle:    tr.Handle,
				Content: []llm.Block{llm.NewTextBlock(fmt.Sprintf(
					"[工具结果已折叠：约 %d token。完整内容可用 tool_output_read 取回（句柄 %s）。]",
					tokens, tr.Handle))},
			}
			blocks = append(blocks, stub)
			seg.Trims = append(seg.Trims, Trim{
				ItemID: tr.ToolUseID, Tokens: tokens,
				Reason: TrimStubToolResult, Ref: tr.Handle,
			})
			changed = true
		}
		if changed {
			p.history[i] = llm.Message{Role: msg.Role, Content: blocks, Meta: msg.Meta}
		}
	}

	if changed {
		seg.Messages = p.history
		seg.Tokens = CountTokens(p.history) + llm.EstimateText(seg.Text)
		seg.Hash = historyHash(p.history)
	}
}

// rollupHistory 用已生成的滚动摘要替代最老的历史消息。
//
// 本阶段（M1.1）只做「摘要已存在则用它」；摘要的生成本身在 M1.2 实现。
func rollupHistory(p *assemblyPlan) {
	seg := p.seg(SegHistory)
	if seg == nil || p.summary == "" {
		return
	}

	// 保留条数与压缩器共用同一个常量：两边不一致会导致
	// 「摘要说已覆盖 N 条」而组装器保留了不同条数，历史重复或丢失。
	if len(p.history) <= keepRecentTurns {
		return
	}

	dropped := p.history[:len(p.history)-keepRecentTurns]
	p.history = p.history[len(p.history)-keepRecentTurns:]

	droppedTokens := CountTokens(dropped)
	seg.Trims = append(seg.Trims, Trim{
		ItemID: "history-head", Tokens: droppedTokens, Reason: TrimRollupSummary,
	})
	seg.Messages = p.history
	seg.Text = summaryBlock(p.summary)
	seg.Tokens = CountTokens(p.history) + llm.EstimateText(seg.Text)
	seg.Hash = historyHash(p.history) + hashText(p.summary)
}

// dropOldestTurns 丢弃最老的历史轮次（保留最近 4 条）。
func dropOldestTurns(p *assemblyPlan) {
	seg := p.seg(SegHistory)
	if seg == nil {
		return
	}
	const minKeep = 4
	for len(p.history) > minKeep && !p.withinBudget() {
		dropped := p.history[0]
		p.history = p.history[1:]
		seg.Trims = append(seg.Trims, Trim{
			ItemID: dropped.ID, Tokens: CountTokens([]llm.Message{dropped}),
			Reason: TrimDropOldest,
		})
	}
	seg.Messages = p.history
	seg.Tokens = CountTokens(p.history) + llm.EstimateText(seg.Text)
	seg.Hash = historyHash(p.history)
}

// finish 生成最终输出。
func (p *assemblyPlan) finish() Output {
	out := Output{
		System:   joinSystem(p.segs),
		Messages: p.messages(),
		Segments: p.segs,
		Budget: BudgetReport{
			Available: p.in.Caps.Usable(),
			Allocated: map[string]int{},
			Used:      map[string]int{},
		},
	}
	for _, s := range p.segs {
		out.Budget.Allocated[string(s.Kind)] = p.quotas.budgetFor(s.Kind)
		out.Budget.Used[string(s.Kind)] = s.Tokens
		out.TotalTokens += s.Tokens
	}
	out.Budget.TotalUsed = out.TotalTokens
	out.PrefixHash = prefixHash(p.segs)
	out.CacheBreakpoints = cacheBreakpoints(p.segs, p.in.Caps, out.Messages)
	return out
}

// messages 组装最终消息数组：历史 + 当前输入。
func (p *assemblyPlan) messages() []llm.Message {
	out := make([]llm.Message, 0, len(p.history)+len(p.in.LiveTurns)+2)
	if p.summary != "" {
		out = append(out, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.Block{llm.NewTextBlock(summaryBlock(p.summary))},
			Meta:    llm.MessageMeta{Trust: llm.TrustSystem},
		})
	}
	out = append(out, p.history...)

	// 易变段（情节召回、RAG、便签）作为当前用户消息的首个块。
	// 这样它们逐轮变化也不会污染 system 的稳定前缀。
	input := p.in.UserInput
	if volatile := volatilePrefix(p.segs); volatile != "" {
		blocks := make([]llm.Block, 0, 1+len(input.Content))
		blocks = append(blocks, llm.NewTextBlock(volatile))
		blocks = append(blocks, input.Content...)
		input = llm.Message{
			ID: input.ID, Role: input.Role, Content: blocks,
			CreatedAt: input.CreatedAt, Meta: input.Meta,
		}
	}
	out = append(out, input)

	// 本轮工具往返原样附后：它们必须与 tool_use 严格配对，绝不参与裁剪。
	out = append(out, p.in.LiveTurns...)
	return out
}

// joinSystem 把 1-3 段拼成 system 提示，其余段以文本形式并入 notes。
// joinSystem 只拼接**稳定前缀段**（1-3：身份、工具技能目录、画像卡）。
//
// 段 4/5/8（情节召回、RAG、便签）每轮都可能变化，绝不能进 system——
// 那会让 Anthropic 的前缀缓存从第一个字节就失效，成本翻倍。
// 它们由 volatilePrefix 注入到当前用户消息里。
func joinSystem(segs []Segment) string {
	var parts []string
	for _, s := range segs {
		if s.Kind == SegHistory || s.Kind == SegInput {
			continue
		}
		if !s.Frozen {
			continue // 段 4 起是易变区，不进稳定前缀
		}
		if s.Text == "" {
			continue
		}
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// volatilePrefix 把易变段（情节召回、RAG、便签）拼成一段文本，
// 作为当前用户消息的首个内容块。
//
// 放在消息里而不是 system：追加区变化不会影响前缀缓存，
// 且这些内容本来就只在「这一轮」有意义。
func volatilePrefix(segs []Segment) string {
	var parts []string
	for _, s := range segs {
		if s.Frozen {
			continue
		}
		if s.Kind == SegHistory || s.Kind == SegInput {
			continue
		}
		if s.Text == "" {
			continue
		}
		parts = append(parts, s.Text)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// prefixHash 是前 3 段的哈希拼接，用于检测前缀缓存是否失效。
func prefixHash(segs []Segment) string {
	var h strings.Builder
	for _, s := range segs {
		if s.Frozen {
			h.WriteString(s.Hash)
		}
	}
	return hashText(h.String())
}

// cacheBreakpoints 计算 Anthropic 的缓存断点位置。
//
// 候选为「第 3 段末」「历史段末」「当前输入前一条」，从后往前取，
// 要求前缀累计 token 达到模型的最小可缓存长度，最多 4 个。
func cacheBreakpoints(segs []Segment, caps llm.Capabilities, msgs []llm.Message) []int {
	if !caps.PrefixCache || len(msgs) == 0 {
		return nil
	}
	minTokens := caps.MinCacheTokens
	if minTokens <= 0 {
		minTokens = 1024
	}

	// 前缀累计到前 3 段末尾的 token 数
	prefixTokens := 0
	for _, s := range segs {
		if s.Frozen {
			prefixTokens += s.Tokens
		}
	}
	if prefixTokens < minTokens {
		return nil
	}

	// 断点落在最后一条消息的最后一个块上：让整个前缀被缓存。
	// 返回消息索引（调用方负责在该消息末尾打 cache_control）。
	idx := len(msgs) - 2 // 当前输入之前的最后一条
	if idx < 0 {
		idx = 0
	}
	return []int{idx}
}

func renderTools(specs []llm.ToolSpec, skillLines []string) string {
	if len(specs) == 0 && len(skillLines) == 0 {
		return ""
	}
	var b strings.Builder
	if len(specs) > 0 {
		b.WriteString("# 可用工具\n")
		for _, s := range specs {
			fmt.Fprintf(&b, "- %s：%s\n", s.Name, s.Description)
		}
	}
	if len(skillLines) > 0 {
		b.WriteString("\n# 可用技能\n")
		for _, l := range skillLines {
			b.WriteString("- " + l + "\n")
		}
	}
	return b.String()
}

func renderRAG(chunks []RAGChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# 参考资料\n")
	for i, c := range chunks {
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, c.Title, c.Text)
	}
	return b.String()
}

func summaryBlock(summary string) string {
	return "## 早前对话摘要\n" + summary
}

func segmentTokens(s Segment) int {
	total := llm.EstimateText(s.Text)
	if len(s.Messages) > 0 {
		total += CountTokens(s.Messages)
	}
	return total
}

func segmentHash(s Segment) string {
	if len(s.Messages) > 0 {
		return hashText(fmt.Sprintf("%v", s.Messages))
	}
	return hashText(s.Text)
}

// historyHash 计算历史内容指纹。
//
// 只哈希「角色 + 文本 + 块类型」，不做 %v 格式化——后者会把整段历史
// （长会话下几百 KB）展开成字符串，每步组装都白跑一次。
func historyHash(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteByte(':')
		for _, blk := range m.Content {
			b.WriteString(string(blk.BlockType()))
			b.WriteByte('=')
			if t, ok := blk.(llm.TextBlock); ok {
				b.WriteString(t.Text)
			}
			b.WriteByte('\n')
		}
	}
	return hashText(b.String())
}
