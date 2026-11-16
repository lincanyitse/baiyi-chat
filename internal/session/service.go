// Package session 的会话服务层：把仓储、Provider、账本编排在一起。
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/cost"
	"github.com/lincanyitse/baiyi-chat/internal/infra/idgen"
	"github.com/lincanyitse/baiyi-chat/internal/infra/logx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
	"github.com/lincanyitse/baiyi-chat/internal/skill"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// TurnResult 是一轮的完整结果，供渠道渲染。
type TurnResult struct {
	TurnID     string
	Text       string
	Thinking   string
	ToolCalls  []llm.ToolUseBlock
	Usage      llm.UsageEvent
	Cost       float64
	CostKnown  bool
	Latency    time.Duration
	TTFT       time.Duration
	StopReason llm.StopReason
	Steps      int
	Err        error
}

// StreamCallback 在流式过程中被调用，接收归一化事件。
//
// 渠道自行决定如何渲染：文本增量、思考增量、工具调用进行时、块边界。
// 不要把事件压扁成 (kind, delta) 字符串——那会丢掉 Block 生命周期与
// 工具调用，Web 渠道上线时又得改签名并波及所有实现。
type StreamCallback func(ev llm.Event)

// Service 是渠道访问内核的唯一入口。
type Service struct {
	repo       Repository
	reg        *registry.Registry
	ledger     *cost.Ledger
	log        *slog.Logger
	now        func() time.Time
	outputs    tool.OutputStore
	tools      []tool.Tool
	system     string
	skillLines []string
	skills     *skill.Catalog
	budget     agent.Budget
	memory     *MemoryHooks

	// 历史缓存：避免每轮重新解析全部 content_json（长会话下是 O(n) JSON 解码）。
	// Service 是消息的唯一写入方，任何 append 都会失效对应会话的缓存。
	//
	// 键是 sessionID@leaf。分支浏览会让同一会话积累多个键，所以
	// 每个会话只保留最近若干个——用户来回切分支时旧视图本来也不会
	// 被立刻读回，留着只是让整份消息链钉在内存里。
	mu      sync.Mutex
	history map[string][]llm.Message
	// historyKeys 记录每个会话已缓存的键，按插入顺序，供超限时淘汰。
	historyKeys map[string][]string

	// turns 记录进行中的轮次，供「停止」精确取消某一轮。
	turns *turnRegistry
}

// maxBranchCache 是单个会话最多缓存的分支视图数。
const maxBranchCache = 4

// Options 是会话服务的构造参数。
type Options struct {
	Repo     Repository
	Registry *registry.Registry
	Ledger   *cost.Ledger
	Log      *slog.Logger
	Outputs  tool.OutputStore
	Tools    []tool.Tool
	System   string
	// SkillLines 是技能 L0 目录，进入上下文的稳定前缀段。
	Skills     *skill.Catalog
	SkillLines []string
	Budget     agent.Budget
	// Memory 是记忆子系统。nil 表示未启用，所有记忆功能静默跳过。
	Memory *MemoryHooks
}

// NewService 构造会话服务。logger 为 nil 时使用丢弃型日志器。
func NewService(opts Options) *Service {
	logger := opts.Log
	if logger == nil {
		logger = logx.Discard()
	}
	return &Service{
		repo:        opts.Repo,
		reg:         opts.Registry,
		ledger:      opts.Ledger,
		log:         logger,
		now:         time.Now,
		outputs:     opts.Outputs,
		tools:       opts.Tools,
		system:      opts.System,
		skillLines:  opts.SkillLines,
		skills:      opts.Skills,
		budget:      opts.Budget,
		memory:      opts.Memory,
		history:     make(map[string][]llm.Message),
		historyKeys: make(map[string][]string),
		turns:       newTurnRegistry(),
	}
}

// SetTools 替换本轮可用工具（供 bootstrap 在装配后注入）。
func (s *Service) SetTools(tools []tool.Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

// CurrentTools 返回当前工具集。
func (s *Service) CurrentTools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tool.Tool(nil), s.tools...)
}

// SetRegistry 替换模型注册表，供配置热重载使用。
//
// 进行中的轮次不受影响：AskWith 在开头就把 Provider 取出来存进
// agent.TurnInput，手里握着旧实例直到这一轮跑完。
func (s *Service) SetRegistry(reg *registry.Registry) {
	s.mu.Lock()
	s.reg = reg
	s.mu.Unlock()
}

// CreateSession 新建会话。
func (s *Service) CreateSession(ctx context.Context, title string) (Session, error) {
	now := s.now()
	sess := Session{
		ID:        idgen.New(),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// ListSessions 列出会话。
func (s *Service) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	return s.repo.ListSessions(ctx, limit)
}

// SearchSessions 按条件筛选会话。
func (s *Service) SearchSessions(ctx context.Context, q SessionQuery) (SessionPage, error) {
	return s.repo.SearchSessions(ctx, q)
}

// SearchMessages 在单个会话里搜消息正文。
func (s *Service) SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]StoredMessage, error) {
	return s.repo.SearchMessages(ctx, sessionID, query, limit)
}

// RenameSession 修改会话标题。
func (s *Service) RenameSession(ctx context.Context, id, title string) error {
	return s.repo.UpdateSessionTitle(ctx, id, title)
}

// ArchiveSession 归档会话；archived 为 false 时取消归档。
func (s *Service) ArchiveSession(ctx context.Context, id string, archived bool) error {
	if !archived {
		return s.repo.ArchiveSession(ctx, id, nil)
	}
	now := s.now()
	return s.repo.ArchiveSession(ctx, id, &now)
}

// DeleteSession 软删除会话，并清掉它的历史缓存。
func (s *Service) DeleteSession(ctx context.Context, id string) error {
	if err := s.repo.DeleteSession(ctx, id); err != nil {
		return err
	}
	s.invalidateHistory(id)
	return nil
}

// CurrentModel 返回 chat 任务的当前默认模型引用。
func (s *Service) CurrentModel() (registry.ModelRef, error) {
	_, ref, err := s.reg.Resolve("chat")
	return ref, err
}

// Ask 执行一轮对话：落库用户消息 → 调模型流式 → 落库助手消息 → 记账。
// Ask 执行一轮对话。
//
// 职责边界：session 只做「建 turn → 组装输入 → 调 agent → 落终态」，
// 模型调用、工具编排、预算与取消传播全部交给 agent.Engine。
func (s *Service) Ask(ctx context.Context, sessionID, userText string, cb StreamCallback) (TurnResult, error) {
	return s.AskWith(ctx, sessionID, userText, cb, AskOptions{})
}

// AskOptions 是单轮调用的可选参数。
type AskOptions struct {
	// Approver 处理需要人工确认的工具调用。
	Approver tool.Approver
	// SessionApproved 报告工具是否已被本会话放行。
	SessionApproved func(name string) bool
	// MarkApproved 在用户批准后记录授权。
	MarkApproved func(name string, action tool.ApprovalAction)
	// ResumeTurnID 非空表示这是续跑：复用已有轮次，不再追加用户消息
	// （首次执行时已经落库过）。
	ResumeTurnID string
	// PreparedTurnID 非空表示轮次已由调用方创建（Web 的先建后跑模式）。
	PreparedTurnID string
	// ParentID 非空表示这条用户消息挂在指定消息之下，形成新分支。
	// 重新生成时传被重生成回答的父节点；编辑消息时传原消息的父节点。
	ParentID string
	// LeafID 指定本轮要读取哪条分支的历史。
	// 空表示沿用会话最新消息作为叶子。
	LeafID string
	// SkipUserMessage 为真时不追加用户消息——它已经在 transcript 上了
	// （重新生成复用原提问，续跑也不该重复写）。
	SkipUserMessage bool
	// Attachments 是随本条用户消息一起落库的附加块（图片、文件）。
	// 它们排在文本块之前，与多数协议的惯例一致。
	Attachments []llm.Block
}

// AskWith 执行一轮对话，可指定审批交互。
func (s *Service) AskWith(
	ctx context.Context, sessionID, userText string,
	cb StreamCallback, opts AskOptions,
) (TurnResult, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return TurnResult{}, err
	}

	turnID := opts.ResumeTurnID
	resuming := turnID != ""
	switch {
	case resuming:
		// 复用已有轮次，不重复创建
	case opts.PreparedTurnID != "":
		// 调用方已建好轮次（Web 的先返回 turn_id、再后台跑）
		turnID = opts.PreparedTurnID
	default:
		turn, err := s.startTurn(ctx, sessionID, "")
		if err != nil {
			return TurnResult{}, err
		}
		turnID = turn.ID
	}

	// ref 在 finish 闭包里用于算成本，而选型失败时它还没有值。
	// 先声明在这里，让「选型失败」也能走 finish 把轮次置为 failed——
	// 否则轮次会永远停在 running，前端只能干等。
	var ref registry.ModelRef

	finish := func(status TurnStatus, errMsg string, res agent.Result) (TurnResult, error) {
		now := s.now()
		if err := s.repo.UpdateTurnStatus(context.WithoutCancel(ctx), turnID, status, errMsg, &now); err != nil {
			s.log.Error("更新轮次状态失败", "turn", turnID, "status", status, "err", err)
		}
		if err := s.repo.TouchSession(context.WithoutCancel(ctx), sessionID, now); err != nil {
			s.log.Warn("更新会话时间失败", "session", sessionID, "err", err)
		}
		out := TurnResult{
			TurnID: res.TurnID, Text: res.Text, Thinking: res.Thinking,
			ToolCalls: res.ToolCalls, Usage: res.Usage,
			Latency: res.Latency, TTFT: res.TTFT,
			StopReason: res.StopReason, Steps: res.Steps, Err: res.Err,
		}
		if fee, known := s.ledger.Compute(ref.Provider, ref.Model,
			res.Usage.Input, res.Usage.Output, res.Usage.Cached); known {
			out.Cost, out.CostKnown = fee, true
		}
		if errMsg != "" {
			return out, errors.New(errMsg)
		}
		return out, nil
	}

	// 为本轮派生可取消的 ctx：停止按钮 / Ctrl-C 只影响这一轮，
	// 不波及会话里的其他操作。
	//
	// 注意用 = 而不是 :=：finish 闭包捕获的是这里的 ctx，
	// 如果遮蔽出一个新变量，它看到的仍是未取消的旧 ctx，
	// 于是「用户点了停止」会被记成「轮次失败」。
	var endTurn func()
	ctx, endTurn = s.turns.begin(ctx, turnID)
	defer endTurn()

	history, err := s.HistoryAt(ctx, sessionID, opts.LeafID)
	if err != nil {
		// 用户在准备阶段就点了停止：这是取消，不是失败。
		if errors.Is(ctx.Err(), context.Canceled) {
			return finish(TurnCanceled, "", agent.Result{TurnID: turnID})
		}
		return finish(TurnFailed, err.Error(), agent.Result{TurnID: turnID})
	}

	// 选型放在 finish 定义之后：失败时也要把轮次置为 failed，
	// 否则它会永远停在 running，前端只能干等。
	// 典型场景：配置里没有任何 Provider，或指定的 Provider 已被删除。
	provider, ref, err := s.resolveForSession(sess)
	if err != nil {
		return finish(TurnFailed, err.Error(), agent.Result{TurnID: turnID})
	}

	// 历史接近配额时先生成摘要，避免下一轮直接触发丢弃。
	summary := s.maybeCompress(ctx, sessionID, history, provider, ref.Model)

	// 用户消息先落库，保证即使本轮崩溃也有记录。
	//
	// 续跑与重新生成时用户消息已经在链上，不能再写一条：前者是首次
	// 执行时写过，后者是复用了原有的提问。两者落到同一个判断里。
	//
	// 用 WithoutCancel：用户发完消息立刻点停止，这条消息仍然该留在 transcript 里。
	// 用可取消的 ctx 会让「停止」变成「消息丢失」。
	var userMsgID string
	if resuming || opts.SkipUserMessage {
		// 消息已在链上：助手回答要挂在它下面，而不是挂在空处
		userMsgID = opts.ParentID
	} else {
		// 普通续聊接在当前分支末端；显式 ParentID（编辑）优先。
		parentID := opts.ParentID
		if parentID == "" && len(history) > 0 {
			parentID = history[len(history)-1].ID
		}
		// 附件排在文本之前：多数协议要求图片/文件先于文字出现
		blocks := make([]llm.Block, 0, len(opts.Attachments)+1)
		blocks = append(blocks, opts.Attachments...)
		blocks = append(blocks, llm.NewTextBlock(userText))

		id, err := s.appendMessage(context.WithoutCancel(ctx), sessionID, llm.RoleUser,
			blocks, parentID)
		if err != nil {
			return finish(TurnFailed, err.Error(), agent.Result{TurnID: turnID})
		}
		userMsgID = id
	}

	engine := agent.New(agent.Options{
		Store:   &storeAdapter{svc: s, repo: s.repo},
		Log:     s.log,
		Clock:   s.now,
		Outputs: s.outputs,
	})

	// 记忆召回：纯 SQL，毫秒级。画像卡进稳定前缀，情节进当前消息。
	profileCard, episodes := s.RecallMemory(ctx, userText)

	res, runErr := engine.Run(ctx, agent.TurnInput{
		TurnID:          turnID,
		SessionID:       sessionID,
		UserText:        userText,
		History:         history,
		Provider:        provider,
		Model:           ref.Model,
		Fallbacks:       s.fallbacks(ref),
		ReportResult:    s.reg.RecordResult,
		System:          s.system,
		Tools:           s.tools,
		ToolChoice:      llm.ToolChoiceAuto,
		Budget:          s.budget,
		Approver:        opts.Approver,
		SessionApproved: opts.SessionApproved,
		MarkApproved:    opts.MarkApproved,
		Snapshots:       &snapshotSink{repo: s.repo, log: s.log},
		SkillLines:      s.skillLines,
		Notes:           s.autoSkillNotes(userText),
		Summary:         summary,
		ProfileCard:     profileCard,
		Episodes:        episodes,
	}, agent.StreamCallback(cb))

	// 助手消息落库。
	//
	// 工具调用也一并存下来：它们是这一轮真实做过的事，用户回看历史时
	// 应该看到「查了知识库」「写了文件」，而不是凭空冒出个结论。
	// 只存 tool_use，不存 tool_result——结果可能很大，且对阅读价值低。
	if res.Text != "" || res.Thinking != "" || len(res.ToolCalls) > 0 {
		blocks := make([]llm.Block, 0, 2+len(res.ToolCalls))
		if res.Thinking != "" {
			blocks = append(blocks, llm.NewThinkingBlock(res.Thinking, ""))
		}
		for _, call := range res.ToolCalls {
			blocks = append(blocks, call)
		}
		if res.Text != "" {
			blocks = append(blocks, llm.NewTextBlock(res.Text))
		}
		if _, err := s.appendMessage(context.WithoutCancel(ctx), sessionID,
			llm.RoleAssistant, blocks, userMsgID); err != nil {
			s.log.Error("助手消息落库失败", "turn", turnID, "err", err)
		}
	}

	// 记忆抽取：只从用户话语里抽，助手回复不作为记忆来源。
	// 异步执行：写库会碰 SQLite 单写者锁，不该挡住用户看到回复后
	// 立刻发下一句。抽取本身是纯规则（微秒级），失败只记日志。
	if s.memory != nil {
		go s.CaptureMemory(context.WithoutCancel(ctx), sessionID, turnID, userText, res.Text)
	}

	// 首轮自动命名。
	//
	// 先生成、失败退回截断：截断常常得到「帮我看看这个」这种分不清的
	// 标题，而模型生成的是「修复 SSE 断线续传」。但命名是锦上添花，
	// 模型不可用时绝不能因此让这一轮失败。
	if sess.Title == "" && userText != "" {
		title := truncateTitle(userText)
		if generated, err := s.GenerateTitle(context.WithoutCancel(ctx), userText); err != nil {
			s.log.Debug("自动命名失败，退回截断", "session", sessionID, "err", err)
		} else {
			title = generated
		}
		if err := s.repo.UpdateSessionTitle(context.WithoutCancel(ctx), sessionID, title); err != nil {
			s.log.Warn("更新会话标题失败", "session", sessionID, "err", err)
		}
	}

	s.log.Info("轮次完成",
		"turn", turnID, "session", sessionID,
		"provider", ref.Provider, "model", ref.Model,
		"stop", res.StopReason, "steps", res.Steps,
		"in", res.Usage.Input, "out", res.Usage.Output, "cached", res.Usage.Cached,
		"ttft_ms", res.TTFT.Milliseconds(), "latency_ms", res.Latency.Milliseconds())

	if runErr != nil {
		// 用户主动停止与真正的失败要区分开：
		// canceled 不是错误，UI 不该显示红色的失败提示。
		//
		// 不能只靠 errors.Is：取消可能发生在流式读取或工具执行里，
		// 错误被适配器包装后不一定还能 unwrap 到 context.Canceled。
		// 同时看 turn 自己的 ctx 是否被取消——那是「用户点了停止」
		// 最直接的证据。
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return finish(TurnCanceled, "", res)
		}
		return finish(TurnFailed, runErr.Error(), res)
	}
	return finish(TurnDone, "", res)
}

func (s *Service) appendMessage(ctx context.Context, sessionID string, role llm.Role, blocks []llm.Block, parentID string) (string, error) {
	seq, err := s.repo.NextSeq(ctx, sessionID)
	if err != nil {
		return "", err
	}
	id := idgen.New()
	if err := s.repo.AppendMessage(ctx, StoredMessage{
		ID: id, SessionID: sessionID, Seq: seq, ParentID: parentID,
		Role: role, Content: blocks, Trust: llm.TrustUser, CreatedAt: s.now(),
	}); err != nil {
		return "", err
	}
	s.invalidateHistory(sessionID)
	return id, nil
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= 24 {
		return s
	}
	return string(r[:24]) + "…"
}

// SessionSummary 汇总会话的用量与费用。
func (s *Service) SessionSummary(ctx context.Context, sessionID string) (cost.Summary, error) {
	turns, err := s.repo.ListTurns(ctx, sessionID)
	if err != nil {
		return cost.Summary{}, err
	}
	ids := make([]string, 0, len(turns))
	for _, t := range turns {
		ids = append(ids, t.ID)
	}
	return s.ledger.SessionSummary(ctx, ids)
}

// ProviderNames 返回全部已配置 Provider 名称。
func (s *Service) ProviderNames() []string { return s.reg.Names() }

// ListModels 查询某个 Provider 服务端的模型列表。
func (s *Service) ListModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error) {
	return s.reg.ListModels(ctx, providerName)
}

// ConfiguredModels 返回某 Provider 在配置中声明的模型 ID 集合。
func (s *Service) ConfiguredModels(providerName string) map[string]bool {
	return s.reg.ConfiguredModels(providerName)
}

// ResumableTurn 返回会话中可恢复的轮次。
//
// 只报告状态，不改动数据——真正的恢复由 ResumeTurn 执行。
func (s *Service) ResumableTurn(ctx context.Context, sessionID string) (Turn, bool, error) {
	return s.repo.FindResumable(ctx, sessionID)
}

// ResumeTurn 续跑一个未完成的轮次。
//
// 恢复算法是纯函数式的：transcript 是 append-only 步骤日志，
// tool_results 是幂等缓存，因此只要「重放 transcript 并按 call id 跳过已完成调用」
// 就能回到中断点。不依赖持久化的状态机快照。
//
// 实际逻辑在 ResumeTurnWith；这里只是不带审批设置的便捷入口，
// 供 REPL 使用（REPL 的审批走终端 y/n，不进 AskOptions）。
func (s *Service) ResumeTurn(ctx context.Context, sessionID, turnID string, cb StreamCallback) (TurnResult, error) {
	return s.ResumeTurnWith(ctx, sessionID, turnID, cb, AskOptions{})
}

// lastUserText 返回历史中最后一条用户消息的文本。
func lastUserText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			if t := llm.TextOf(msgs[i]); t != "" {
				return t
			}
		}
	}
	return ""
}

// LatestContext 返回某轮次最近一次上下文快照，供 /context 命令展示。
func (s *Service) LatestContext(ctx context.Context, sessionID string) (string, string, int, error) {
	turns, err := s.repo.ListTurns(ctx, sessionID)
	if err != nil {
		return "", "", 0, err
	}
	if len(turns) == 0 {
		return "", "", 0, fmt.Errorf("会话还没有轮次")
	}
	last := turns[len(turns)-1]

	snap, err := s.repo.GetContextSnapshot(ctx, last.ID, -1)
	if err != nil {
		return "", "", 0, fmt.Errorf("该轮次没有上下文快照: %w", err)
	}
	segs, err := json.Marshal(snap.Segments)
	if err != nil {
		return "", "", 0, fmt.Errorf("序列化快照片段: %w", err)
	}
	budget, err := json.Marshal(snap.Budget)
	if err != nil {
		return "", "", 0, fmt.Errorf("序列化快照预算: %w", err)
	}
	return string(segs), string(budget), snap.Step, nil
}

// SkillInfo 是供界面展示的技能条目。
type SkillInfo struct {
	Name        string
	Description string
}

// Skills 返回当前可用技能。
func (s *Service) Skills() []SkillInfo {
	if s.skills == nil {
		return nil
	}
	digests := s.skills.Digests()
	out := make([]SkillInfo, 0, len(digests))
	for _, d := range digests {
		out = append(out, SkillInfo{Name: d.Name, Description: d.Description})
	}
	return out
}

// SkillsRoot 返回技能目录。
func (s *Service) SkillsRoot() string {
	if s.skills == nil {
		return "(未配置)"
	}
	return s.skills.Root()
}

// SkillLines 返回当前进入上下文的技能 L0 目录。
// 供测试断言「禁用后确实从上下文里消失」。
func (s *Service) SkillLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.skillLines...)
}

// ReloadSkills 强制重新扫描技能目录，并刷新常驻的 L0 目录。
func (s *Service) ReloadSkills() error {
	if s.skills == nil {
		return fmt.Errorf("技能目录未配置")
	}
	if err := s.skills.ForceReload(); err != nil {
		return err
	}

	lines := make([]string, 0)
	for _, d := range s.skills.Digests() {
		lines = append(lines, d.Name+"："+d.Description)
	}

	s.mu.Lock()
	s.skillLines = lines
	s.mu.Unlock()
	return nil
}

// autoSkillNotes 返回命中确定性触发词的技能正文。
//
// 放在 Notes 段而不是 system：命中是逐轮的、会变的内容，
// 写进 system 会让稳定前缀失效，得不偿失。
func (s *Service) autoSkillNotes(userText string) string {
	if s.skills == nil {
		return ""
	}
	activated := s.skills.AutoActivate(userText)
	if len(activated) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# 已激活技能（命中触发词，直接按以下流程执行）\n")
	for _, sk := range activated {
		b.WriteString("\n## " + sk.Spec.Name + "\n")
		b.WriteString(sk.Body)
		b.WriteString("\n")
	}
	return b.String()
}

// ProvidersHealth 返回各 Provider 的健康摘要，供 /health 展示。
func (s *Service) ProvidersHealth() []registry.HealthReport {
	return s.reg.Health()
}

// fallbacks 返回除了首选之外的其余候选，供 agent 在失败时降级。
func (s *Service) fallbacks(primary registry.ModelRef) []agent.ProviderChoice {
	chain := s.reg.ResolveChain("chat")
	out := make([]agent.ProviderChoice, 0, len(chain))
	for _, c := range chain {
		if c.Ref == primary {
			continue
		}
		out = append(out, agent.ProviderChoice{Provider: c.Provider, Model: c.Ref.Model})
	}
	return out
}

// resolveForSession 优先用会话指定的模型，回退到任务路由。
//
// 会话覆盖不参与健康度降级：用户明确指定了 A，自动降级到 B 会让他
// 以为是配置没生效。若 A 不可用，直接返回错误让人看见。
func (s *Service) resolveForSession(sess Session) (llm.Provider, registry.ModelRef, error) {
	if sess.ModelRef == "" {
		return s.reg.Resolve("chat")
	}

	ref, err := registry.ParseRef(sess.ModelRef)
	if err != nil {
		return nil, registry.ModelRef{}, fmt.Errorf("会话指定的模型 %q 格式有误: %w", sess.ModelRef, err)
	}
	p, ok := s.reg.Lookup(ref.Provider)
	if !ok {
		return nil, ref, fmt.Errorf("会话指定的 Provider %q 未配置，请在设置里重新选择模型", ref.Provider)
	}
	return p, ref, nil
}

// SessionModel 返回本会话实际会用的模型引用。
//
// 与 resolveForSession 同源，但不构造 Provider——渠道层只需要模型名
// 做能力检查与展示。
func (s *Service) SessionModel(sess Session) (registry.ModelRef, bool) {
	if sess.ModelRef != "" {
		ref, err := registry.ParseRef(sess.ModelRef)
		if err != nil {
			return registry.ModelRef{}, false
		}
		return ref, true
	}
	_, ref, err := s.reg.Resolve("chat")
	if err != nil {
		return registry.ModelRef{}, false
	}
	return ref, true
}

// ModelCapabilities 返回某模型的能力声明。
func (s *Service) ModelCapabilities(ref registry.ModelRef) llm.Capabilities {
	p, ok := s.reg.Lookup(ref.Provider)
	if !ok {
		return llm.Capabilities{}
	}
	return p.Capabilities(ref.Model)
}

// ValidateSessionModel 检查会话指定的模型是否可用，供渠道层在提交时预检。
//
// 选型本身在后台跑时才发生；如果不预检，用户指定了一个已删除的
// provider 会在订阅 SSE 之后才看到错误，体验上像是「点了没反应」。
func (s *Service) ValidateSessionModel(sess Session) error {
	// 空 ModelRef 也要验证默认路由——否则「一个 Provider 都没配」时
	// 预检会放行，等后台选型才失败，用户看到的是一次卡住的轮次。
	_, _, err := s.resolveForSession(sess)
	return err
}

// SetSessionModel 设置会话级模型覆盖。空串表示恢复跟随路由。
func (s *Service) SetSessionModel(ctx context.Context, sessionID, modelRef string) error {
	if modelRef != "" {
		// 提前校验格式与 provider 是否存在，避免用户等到下一轮才发现
		if _, err := registry.ParseRef(modelRef); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if err := s.repo.UpdateSessionModel(ctx, sessionID, modelRef); err != nil {
		return err
	}
	s.invalidateHistory(sessionID)
	return nil
}

// maybeCompress 在历史接近配额时生成滚动摘要。
//
// 压缩是尽力而为：失败只记日志，不影响本轮对话——宁可让组装器
// 按降级链处理，也不要让摘要问题阻塞用户。
func (s *Service) maybeCompress(
	ctx context.Context, sessionID string,
	history []llm.Message, provider llm.Provider, model string,
) string {
	if len(history) <= keepRecentForCompress {
		// 顺带把已有摘要带出来（上次压缩过）
		if st, ok, err := s.repo.LoadSummary(ctx, sessionID); err == nil && ok {
			return st.Summary
		}
		return ""
	}

	caps := provider.Capabilities(model)
	// 配额从 contextx 取，不在这里硬编码——否则 M2 重分配配额时两处会漂移。
	quota := contextx.HistoryQuota(caps)
	historyTokens := contextx.CountTokens(history)

	if !contextx.ShouldCompress(historyTokens, quota) {
		if st, ok, err := s.repo.LoadSummary(ctx, sessionID); err == nil && ok {
			return st.Summary
		}
		return ""
	}

	c := contextx.NewCompressor(s, &llmSummarizer{reg: s.reg})
	st, cut, err := c.Compress(ctx, sessionID, history)
	if err != nil {
		s.log.Warn("生成滚动摘要失败", "session", sessionID, "err", err)
		return ""
	}

	s.log.Info("生成滚动摘要",
		"session", sessionID, "covered", cut, "tokens_in", st.TokensIn)
	return st.Summary
}

// keepRecentForCompress 是触发压缩的最小历史条数。
const keepRecentForCompress = 20
