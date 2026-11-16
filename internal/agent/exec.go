package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// outcome 是单个工具调用的执行结果。
type outcome struct {
	block llm.Block
}

// executeCalls 执行一批工具调用。
//
// 并行策略（架构纲要 §4.3）：
//   - 同一条 assistant 消息里的多个调用视为互相独立，可并行
//   - 结果按下标写预分配切片，天然保持 tool_use 原始顺序
//   - 任一工具 panic 只影响自己，转成错误结果继续
//   - Provider 不支持并行时退化为串行，走同一代码路径
func (t *turnState) executeCalls(ctx context.Context, calls []llm.ToolUseBlock) []llm.Block {
	if len(calls) == 0 {
		return nil
	}

	// 串行路径：能力声明不支持并行，或只有一个调用。
	if len(calls) == 1 || !t.parallelOK() {
		out := make([]llm.Block, len(calls))
		for i, call := range calls {
			out[i] = t.safeRunTool(ctx, call)
		}
		return out
	}

	out := make([]outcome, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c llm.ToolUseBlock) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					out[idx] = outcome{
						block: errorBlock(c.ID, fmt.Sprintf("工具内部错误: %v", r)),
					}
					t.log.Error("工具 panic 已隔离",
						"turn", t.in.TurnID, "tool", c.Name, "panic", fmt.Sprint(r))
				}
			}()
			out[idx] = outcome{block: t.safeRunTool(ctx, c)}
		}(i, call)
	}
	wg.Wait()

	blocks := make([]llm.Block, len(calls))
	for i, o := range out {
		blocks[i] = o.block
	}
	return blocks
}

// parallelOK 报告当前模型是否支持并行工具调用。
func (t *turnState) parallelOK() bool {
	caps := t.in.Provider.Capabilities(t.in.Model)
	// 能力未声明时保守地串行执行
	return caps.ParallelTools
}

// safeRunTool 在 recover 保护下执行单个工具。
//
// panic 也必须写入幂等缓存——否则该调用在续跑时会被重复执行，
// 「同一 call id 只执行一次」的不变量就破了。
func (t *turnState) safeRunTool(ctx context.Context, call llm.ToolUseBlock) (block llm.Block) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("错误: 工具内部错误: %v", r)
			t.log.Error("工具 panic 已隔离",
				"turn", t.in.TurnID, "tool", call.Name, "panic", fmt.Sprint(r))
			block = t.recordToolResult(ctx, call, msg, true, "", 0, hashArgs(call.Input))
		}
	}()
	return t.runTool(ctx, call)
}

// needsApproval 判断这批调用是否需要人工审批。
//
// Approver 为 nil 不代表「无需审批」，而是「无人可问」。此时凡是
// 必须确认的调用都要报出来，由 execute 统一拒绝——直接放行会让
// 危险操作在无人值守下静默执行（FR-TOL-002 的底线）。
func (t *turnState) needsApproval(calls []llm.ToolUseBlock) ([]tool.ApprovalCall, bool) {
	var pending []tool.ApprovalCall
	for _, call := range calls {
		tl, ok := findTool(t.in.Tools, call.Name)
		if !ok {
			continue // 未知工具会直接回填错误，无需审批
		}
		spec := tl.Spec()

		if !t.mustConfirm(spec) {
			continue
		}
		pending = append(pending, tool.ApprovalCall{
			CallID:  call.ID,
			Name:    spec.Name,
			Args:    call.Input,
			Risk:    spec.Risk,
			Summary: summarizeArgs(call.Input),
		})
	}
	return pending, len(pending) > 0
}

// mustConfirm 判断一个工具调用是否必须走人工确认。
//
// 三个分支按优先级排列，读起来就是审批策略本身：
//
//	只读        → 永不确认（不改变外部状态）
//	危险        → 永远确认（不可逆，不享受任何已授予的许可，FR-TOL-002）
//	写          → 已授权且本轮未被污染时跳过
//
// 污点追踪（架构纲要 §13.3）：一旦本轮消费过网页、RAG 片段或 MCP
// 工具描述，先前授予的「永久允许」不再可信——外部内容可能诱导模型
// 调用写操作。危险操作是唯一不妥协的一条：不可逆操作不该因为
// 「上次点过允许」就在无人值守时执行。
func (t *turnState) mustConfirm(spec tool.Spec) bool {
	if spec.Risk == tool.RiskRead {
		return false
	}
	if spec.Risk == tool.RiskDangerous {
		return true
	}
	return !(t.approved(spec.Name) && !t.tainted())
}

// approved 报告该工具是否已被本会话或永久授权。
func (t *turnState) approved(name string) bool {
	if t.in.SessionApproved == nil {
		return false
	}
	return t.in.SessionApproved(name)
}

func (t *turnState) tainted() bool {
	return t.taint != nil && t.taint.IsTainted()
}

// summarizeArgs 生成参数的一行摘要，供审批界面展示。
func summarizeArgs(in []byte) string {
	const max = 100
	s := string(in)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
