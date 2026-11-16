// Package repl 是内核阶段的最小调试 REPL。
// 只承担调试职责，不追求好用；正式 CLI 在 M5。
package repl

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/contextx"
	"github.com/lincanyitse/baiyi-chat/internal/infra/logx"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// REPL 是命令行交互循环。
type REPL struct {
	svc      *session.Service
	in       io.Reader
	out      io.Writer
	log      *slog.Logger
	sc       *bufio.Scanner
	approver *terminalApprover
	session  string
	model    string
	kb       Knowledge
	mem      Memory
}

// SetKnowledge 注入知识库能力。未注入时 /kb 命令报告未启用，
// 对话本身照常进行。
func (r *REPL) SetKnowledge(k Knowledge) { r.kb = k }

// SetMemory 注入记忆能力。
func (r *REPL) SetMemory(m Memory) { r.mem = m }

// New 构造 REPL。logger 记录系统事件（会话切换、命令错误），
// 对话内容仍只走 out——UI 输出不进日志文件，见 logx.New 的说明。
func New(svc *session.Service, in io.Reader, out io.Writer, logger *slog.Logger) *REPL {
	if logger == nil {
		logger = logx.Discard()
	}
	return &REPL{svc: svc, in: in, out: out, log: logger}
}

// Run 启动交互循环，直到用户退出。
func (r *REPL) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := r.ensureSession(ctx); err != nil {
		return err
	}

	r.printf("白泽 REPL（调试用）。输入 /help 查看命令，/exit 退出。\n")
	r.printf("当前会话：%s\n\n", r.session)

	sc := bufio.NewScanner(r.in)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	// 审批与对话共用同一个扫描器：对话期间主循环不读输入，不会抢占。
	r.sc = sc
	r.approver = newTerminalApprover(sc, r.printf)

	for {
		r.printf("\n> ")
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := r.handleCommand(ctx, line); quit {
				return nil
			}
			continue
		}
		r.ask(ctx, line)
	}
}

// ensureSession 复用最近有内容的会话，没有才新建。
//
// 每次启动都新建会让会话列表无限膨胀，用户找不到上次的对话。
func (r *REPL) ensureSession(ctx context.Context) error {
	if r.session != "" {
		return nil
	}

	// 找最近一个有消息的会话
	if list, err := r.svc.ListSessions(ctx, 20); err == nil {
		for _, s := range list {
			if msgs, err := r.svc.History(ctx, s.ID); err == nil && len(msgs) > 0 {
				r.session = s.ID
				if ref, err := r.svc.CurrentModel(); err == nil {
					r.model = ref.String()
				}
				return nil
			}
		}
	}

	sess, err := r.svc.CreateSession(ctx, "")
	if err != nil {
		return err
	}
	r.session = sess.ID
	if ref, err := r.svc.CurrentModel(); err == nil {
		r.model = ref.String()
	}
	return nil
}

func (r *REPL) ask(ctx context.Context, text string) {
	start := time.Now()
	// 状态标记用 \r 覆盖写；正文一开始就清掉标记行，
	// 否则思考阶段刷新的标记会覆盖已输出的正文。
	printedText := false
	thinkingShown := false

	cb := func(ev llm.Event) {
		switch e := ev.(type) {
		case llm.ThinkingDeltaEvent:
			if !thinkingShown && !printedText {
				r.printf("\r[思考中…]")
				thinkingShown = true
			}
		case llm.TextDeltaEvent:
			if !printedText {
				if thinkingShown {
					r.printf("\r          \r")
				}
				printedText = true
			}
			r.printf("%s", e.Text)
		case llm.ToolCallReadyEvent:
			// M1 起会真正执行工具；M0 只提示模型请求了调用。
			r.printf("\n[工具调用: %s]\n", e.Name)
		}
	}

	res, err := r.svc.AskWith(ctx, r.session, text, cb, session.AskOptions{
		Approver:        r.approver,
		SessionApproved: r.approver.IsApproved,
		MarkApproved:    r.approver.MarkApproved,
	})
	r.printf("\n")
	if err != nil {
		r.printf("错误: %v\n", err)
		return
	}
	r.printf("%s\n", footer(res, time.Since(start)))
}

func footer(res session.TurnResult, elapsed time.Duration) string {
	costStr := "价格未配置"
	if res.CostKnown {
		costStr = fmt.Sprintf("¥%.6f", res.Cost)
	}
	return fmt.Sprintf(
		"--- tokens: in %d / out %d / cached %d | %s | %.1fs (ttft %.2fs) | %s",
		res.Usage.Input, res.Usage.Output, res.Usage.Cached,
		costStr, elapsed.Seconds(), res.TTFT.Seconds(), res.StopReason,
	)
}

func (r *REPL) handleCommand(ctx context.Context, line string) bool {
	cmd, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	arg = strings.TrimSpace(arg)

	switch cmd {
	case "exit", "quit", "q":
		return true
	case "help":
		r.printf(`命令：
  /exit            退出
  /sessions        列出会话
  /switch <id>     切换会话（短 ID 或完整 ID）
  /new             新建会话
  /model           查看当前模型
  /models [名称]   列出服务端可用模型（* 为已配置能力与价格的）
  /cost            查看当前会话用量与费用
  /history         查看当前会话消息条数
  /resume          续跑中断的轮次
  /kb              知识库：/kb list、/kb add <路径>、/kb search <关键词>、
                   /kb rm <文档短ID>、/kb rebuild、/kb stats
  /memory          记忆：/memory list、/memory card、/memory stats、
                   /memory rm <id>、/memory why <id>、/memory decay
`)
	case "sessions":
		list, err := r.svc.ListSessions(ctx, 20)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		for _, s := range list {
			mark := " "
			if s.ID == r.session {
				mark = "*"
			}
			title := s.Title
			if title == "" {
				title = "(未命名)"
			}
			r.printf("%s %s  %s  %s\n", mark, shortID(s.ID), title, s.UpdatedAt.Format("01-02 15:04"))
		}
	case "switch":
		if arg == "" {
			r.printf("用法: /switch <会话短ID 或完整ID>\n")
			return false
		}
		list, err := r.svc.ListSessions(ctx, 100)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		var matches []session.Session
		for _, s := range list {
			if matchesID(s.ID, arg) {
				matches = append(matches, s)
			}
		}
		switch len(matches) {
		case 0:
			r.printf("未找到会话: %s（用 /sessions 查看可用的短 ID）\n", arg)
		case 1:
			r.session = matches[0].ID
			r.log.Info("切换会话", "session", matches[0].ID)
			r.printf("已切换到 %s\n", shortID(matches[0].ID))
		default:
			r.printf("匹配到 %d 个会话，请提供更精确的 ID：\n", len(matches))
			for _, s := range matches {
				title := s.Title
				if title == "" {
					title = "(未命名)"
				}
				r.printf("  %s  %s\n", shortID(s.ID), title)
			}
		}
	case "new":
		sess, err := r.svc.CreateSession(ctx, "")
		if err != nil {
			r.log.Error("新建会话失败", "err", err)
			r.printf("错误: %v\n", err)
			return false
		}
		r.session = sess.ID
		r.log.Info("新建会话", "session", sess.ID)
		r.printf("新会话 %s\n", shortID(sess.ID))
	case "model":
		if r.model == "" {
			ref, err := r.svc.CurrentModel()
			if err != nil {
				r.printf("错误: %v\n", err)
				return false
			}
			r.model = ref.String()
		}
		r.printf("%s\n", r.model)
	case "models":
		providers := r.svc.ProviderNames()
		if arg != "" {
			providers = []string{arg}
		}
		for _, name := range providers {
			models, err := r.svc.ListModels(ctx, name)
			if err != nil {
				r.printf("[%s] 错误: %v\n", name, err)
				continue
			}
			r.printf("[%s] %d 个模型：\n", name, len(models))
			configured := r.svc.ConfiguredModels(name)
			for _, m := range models {
				mark := " "
				if configured[m.ID] {
					mark = "*"
				}
				r.printf("  %s %s\n", mark, m.ID)
			}
		}
		r.printf("\n（* 表示已在配置中声明能力与价格）\n")
	case "cost":
		sum, err := r.svc.SessionSummary(ctx, r.session)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		r.printf("调用 %d 次 | in %d / out %d / cached %d | 合计 ¥%.6f\n",
			sum.Calls, sum.Input, sum.Output, sum.Cached, sum.Cost)
	case "history":
		msgs, err := r.svc.History(ctx, r.session)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		r.printf("%d 条消息\n", len(msgs))
	case "skills":
		r.listSkills(ctx, arg)
	case "kb":
		r.handleKB(ctx, arg)
	case "memory":
		r.handleMemory(ctx, arg)
	case "health":
		r.printHealth()
	case "context":
		segs, budget, step, err := r.svc.LatestContext(ctx, r.session)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		r.printContext(segs, budget, step)
	case "resume":
		turn, ok, err := r.svc.ResumableTurn(ctx, r.session)
		if err != nil {
			r.printf("错误: %v\n", err)
			return false
		}
		if !ok {
			r.printf("没有可恢复的轮次\n")
			return false
		}
		r.printf("恢复轮次 %s（状态 %s，开始于 %s）\n",
			shortID(turn.ID), turn.Status, turn.StartedAt.Format("01-02 15:04:05"))
		res, err := r.svc.ResumeTurn(ctx, r.session, turn.ID, nil)
		if err != nil {
			r.printf("恢复失败: %v\n", err)
			return false
		}
		r.printf("%s\n", res.Text)
		r.printf("%s\n", footer(res, res.Latency))
	default:
		r.printf("未知命令 /%s，输入 /help 查看\n", cmd)
	}
	return false
}

func (r *REPL) printf(format string, args ...any) {
	fmt.Fprintf(r.out, format, args...)
}

// shortID 返回便于人眼区分的短 ID。
// UUIDv7 的前 8 位是时间戳，同一毫秒创建的会话会完全相同，
// 因此取首段 6 位 + 末段 4 位，保证展示层可区分。
func shortID(id string) string {
	if len(id) < 16 {
		return id
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		return id[:12]
	}
	return parts[0][:6] + "…" + parts[4][:4]
}

// printContext 展示某步的上下文快照：各段 token、配额与裁剪原因。
func (r *REPL) printContext(segsJSON, budgetJSON string, step int) {
	var segs []contextx.Segment
	if err := json.Unmarshal([]byte(segsJSON), &segs); err != nil {
		r.printf("解析快照失败: %v\n", err)
		return
	}
	var budget contextx.BudgetReport
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		r.printf("解析预算失败: %v\n", err)
		return
	}

	r.printf("上下文快照（第 %d 步，可用预算 %d token，实际 %d）\n\n",
		step, budget.Available, budget.TotalUsed)
	r.printf("%-10s %8s %8s  %s\n", "段", "token", "配额", "说明")
	r.printf("%s\n", strings.Repeat("-", 56))

	for _, s := range segs {
		quota := budget.Allocated[string(s.Kind)]
		note := ""
		if s.Frozen {
			note = "稳定前缀"
		}
		if len(s.Trims) > 0 {
			note = fmt.Sprintf("裁剪 %d 项", len(s.Trims))
		}
		r.printf("%-10s %8d %8d  %s\n", s.Kind.Label(), s.Tokens, quota, note)
	}

	// 展开裁剪明细，解释每个决定
	var hasTrims bool
	for _, s := range segs {
		for _, tr := range s.Trims {
			if !hasTrims {
				r.printf("\n裁剪明细：\n")
				hasTrims = true
			}
			ref := ""
			if tr.Ref != "" {
				ref = " → " + tr.Ref
			}
			r.printf("  [%s] %s：%d token%s（%s）\n",
				s.Kind.Label(), tr.ItemID, tr.Tokens, ref, tr.Reason.Label())
		}
	}
	if !hasTrims {
		r.printf("\n未触发任何裁剪。\n")
	}
}

// listSkills 展示技能目录，或执行 reload。
func (r *REPL) listSkills(ctx context.Context, arg string) {
	if arg == "reload" {
		if err := r.svc.ReloadSkills(); err != nil {
			r.printf("重载失败: %v\n", err)
			return
		}
		r.printf("技能已重载\n")
	}

	items := r.svc.Skills()
	if len(items) == 0 {
		r.printf("没有可用技能。在 %s 下创建 <名字>/SKILL.md 即可添加。\n", r.svc.SkillsRoot())
		return
	}
	r.printf("已加载 %d 项技能（来源 %s）：\n\n", len(items), r.svc.SkillsRoot())
	for _, s := range items {
		r.printf("  %-20s %s\n", s.Name, s.Description)
	}
}

// printHealth 展示各 Provider 的健康状态。
func (r *REPL) printHealth() {
	reports := r.svc.ProvidersHealth()
	if len(reports) == 0 {
		r.printf("没有配置任何 Provider\n")
		return
	}
	r.printf("%-24s %8s %8s  %s\n", "Provider", "连续失败", "冷却中", "最后错误")
	r.printf("%s\n", strings.Repeat("-", 72))
	for _, h := range reports {
		cooling := "否"
		if h.Cooling {
			cooling = "是"
		}
		r.printf("%-24s %8d %8s  %s\n", h.Name, h.Failures, cooling, h.LastErr)
	}
}
