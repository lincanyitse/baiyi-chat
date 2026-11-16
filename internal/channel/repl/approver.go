package repl

import (
	"bufio"
	"context"
	"strings"
	"sync/atomic"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// terminalApprover 在终端里逐批确认工具调用。
//
// 与 REPL 共用同一个 bufio.Scanner：对话期间主循环不读输入，
// 因此不会与审批提示抢 stdin。
type terminalApprover struct {
	sc  *bufio.Scanner
	out func(string, ...any)
	// sessionApproved 记录本会话内已被永久放行的工具。
	sessionApproved map[string]bool
	// inputClosed 记录 stdin 是否已结束（管道读完、非交互场景）。
	// 置位后所有审批直接拒绝，不再尝试读取。
	inputClosed atomic.Bool
}

func newTerminalApprover(sc *bufio.Scanner, out func(string, ...any)) *terminalApprover {
	return &terminalApprover{
		sc:              sc,
		out:             out,
		sessionApproved: map[string]bool{},
	}
}

var _ tool.Approver = (*terminalApprover)(nil)

// Request 展示待确认的调用并读取用户选择。
func (a *terminalApprover) Request(ctx context.Context, req tool.ApprovalRequest) (tool.ApprovalDecision, error) {
	a.out("\n需要确认的工具调用：\n")
	for i, c := range req.Calls {
		a.out("  %d. %s（%s）\n", i+1, c.Name, riskLabel(c.Risk))
		if c.Summary != "" {
			a.out("     参数: %s\n", c.Summary)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return tool.ApprovalDecision{Action: tool.Deny, Reason: "已取消"}, ctx.Err()
		default:
		}

		// 输入已结束（管道关闭、非交互场景）：不再打印提示无限重问。
		// 记下这个状态，后续审批直接拒绝——否则模型每次重试都会
		// 刷一遍同样的提示，日志里全是噪声。
		if a.inputClosed.Load() {
			return tool.ApprovalDecision{Action: tool.Deny, Reason: "输入已结束，无人值守"}, nil
		}

		a.out("\n[y] 允许一次  [s] 本会话允许  [a] 永远允许  [n] 拒绝 > ")
		if !a.sc.Scan() {
			a.inputClosed.Store(true)
			if err := a.sc.Err(); err != nil {
				return tool.ApprovalDecision{}, err
			}
			return tool.ApprovalDecision{Action: tool.Deny, Reason: "输入结束"}, nil
		}

		switch strings.ToLower(strings.TrimSpace(a.sc.Text())) {
		case "y", "yes", "":
			return tool.ApprovalDecision{Action: tool.ApproveOnce}, nil
		case "s", "session":
			return tool.ApprovalDecision{Action: tool.ApproveSession}, nil
		case "a", "always":
			// 危险操作不提供永久放行（FR-TOL-002）
			if hasDangerous(req.Calls) {
				a.out("危险操作不支持永久允许，请选择 y 或 s。\n")
				continue
			}
			return tool.ApprovalDecision{Action: tool.ApproveForever}, nil
		case "n", "no":
			return tool.ApprovalDecision{Action: tool.Deny, Reason: "用户拒绝了该操作"}, nil
		default:
			a.out("无法识别的选项，请输入 y/s/a/n。\n")
		}
	}
}

func hasDangerous(calls []tool.ApprovalCall) bool {
	for _, c := range calls {
		if c.Risk == tool.RiskDangerous {
			return true
		}
	}
	return false
}

// MarkApproved 记录用户授予的会话级或永久授权。
func (a *terminalApprover) MarkApproved(name string, action tool.ApprovalAction) {
	if action == tool.ApproveSession || action == tool.ApproveForever {
		a.sessionApproved[name] = true
	}
}

// IsApproved 报告该工具是否已在会话内放行。
func (a *terminalApprover) IsApproved(name string) bool {
	return a.sessionApproved[name]
}

func riskLabel(r tool.RiskLevel) string {
	switch r {
	case tool.RiskRead:
		return "只读"
	case tool.RiskWrite:
		return "写入"
	case tool.RiskDangerous:
		return "危险"
	default:
		return "未知"
	}
}
