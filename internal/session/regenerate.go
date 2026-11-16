package session

// 重新生成与编辑消息。
//
// 两者都遵循同一原则：**不修改已落库的任何消息**。新回答或新输入
// 作为兄弟节点挂在同一父节点下，旧链完整保留。这样「切回旧回答」
// 只是换一个 leaf，不涉及任何数据恢复。

import (
	"context"
	"fmt"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/infra/idgen"
	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// RegenerateOptions 是重新生成的参数。
type RegenerateOptions struct {
	// MessageID 是被重新生成的助手消息。
	MessageID string
	// TurnID 非空时表示调用方已经 PrepareTurn 过，直接复用。
	TurnID string
}

// BranchPlan 是一次分支操作算出来的执行计划。
//
// 类型导出是为了让渠道的接口能写出 Regenerate / EditMessage / RunBranchTurn
// 的签名。字段保持不导出：渠道只把计划从准备阶段交到执行阶段，不拆开看。
//
// 分开算与跑：Web 需要先拿到 turn_id 再订阅事件流，而「这条消息的
// 父节点是什么」「上下文读到哪」这些查库结果在真正执行时还要用。
// 把计划一次算好、两个阶段共用，避免同一条消息被反复查询。
type BranchPlan struct {
	// parentID 是新的用户/助手消息要挂的父节点。
	parentID string
	// leafID 是本轮读取历史的末端。
	leafID string
	// userText 是本轮交给模型的用户话语。
	userText string
	// skipUserMessage 为真时不再写一条用户消息（它已在链上）。
	skipUserMessage bool
}

// Regenerate 针对某条助手消息重新生成回答，返回已建好的轮次与计划。
//
// 流程：原回答的父节点是用户提问 → 以该父节点为 parent 新建一条
// 助手消息。用户提问本身不重复落库——它已经在链上了。
//
// 返回 BranchPlan 给调用方：Web 拿到 turn_id 后要在后台跑，
// 如果它自己再调一次 RegenerateWith，plan 里的两次消息查询就白做了一遍。
func (s *Service) Regenerate(
	ctx context.Context, sessionID string, opts RegenerateOptions,
) (Turn, BranchPlan, error) {
	plan, err := s.regeneratePlan(ctx, sessionID, opts.MessageID)
	if err != nil {
		return Turn{}, BranchPlan{}, err
	}
	turn, err := s.startTurn(ctx, sessionID, opts.TurnID)
	if err != nil {
		return Turn{}, BranchPlan{}, err
	}
	return turn, plan, nil
}

// regeneratePlan 校验消息并算出执行计划。
func (s *Service) regeneratePlan(ctx context.Context, sessionID, messageID string) (BranchPlan, error) {
	orig, err := s.repo.GetMessage(ctx, messageID)
	if err != nil {
		return BranchPlan{}, err
	}
	if orig.SessionID != sessionID {
		return BranchPlan{}, fmt.Errorf("消息 %s 不属于会话 %s: %w", messageID, sessionID, ErrInvalid)
	}
	if orig.Role != llm.RoleAssistant {
		return BranchPlan{}, fmt.Errorf("只能重新生成助手回答，消息 %s 的角色是 %s: %w", messageID, orig.Role, ErrInvalid)
	}
	if orig.ParentID == "" {
		return BranchPlan{}, fmt.Errorf("消息 %s 没有父节点，无法定位提问: %w", messageID, ErrInvalid)
	}

	parent, err := s.repo.GetMessage(ctx, orig.ParentID)
	if err != nil {
		return BranchPlan{}, err
	}

	// 上下文读到原提问为止；用户消息不重复落库（已在链上）
	return BranchPlan{
		parentID:        parent.ID,
		leafID:          parent.ID,
		userText:        llm.TextOfBlocks(parent.Content),
		skipUserMessage: true,
	}, nil
}

// RegenerateWith 执行重新生成并同步跑完。
//
// 与 Regenerate 分开：这个版本给 REPL 与测试用，一步到位。
// 它自己算 plan，因为调用方没有预建的轮次。
func (s *Service) RegenerateWith(
	ctx context.Context, sessionID string, opts RegenerateOptions, cb StreamCallback, ask AskOptions,
) (TurnResult, error) {
	plan, err := s.regeneratePlan(ctx, sessionID, opts.MessageID)
	if err != nil {
		return TurnResult{}, err
	}
	turn, err := s.startTurn(ctx, sessionID, opts.TurnID)
	if err != nil {
		return TurnResult{}, err
	}
	return s.RunBranchTurn(ctx, sessionID, plan, turn.ID, cb, ask)
}

// EditOptions 是编辑用户消息的参数。
type EditOptions struct {
	// MessageID 是被编辑的用户消息。
	MessageID string
	// Text 是编辑后的内容。
	Text string
	// TurnID 非空时复用调用方预建的轮次。
	TurnID string
}

// EditMessage 编辑一条用户消息，产生新分支，返回轮次与计划。
//
// 新消息挂在与原消息相同的父节点下，原消息及其下游保持不变。
// 之后的新回答会挂在编辑后的消息下面——这是「改一句话重来」的语义。
func (s *Service) EditMessage(
	ctx context.Context, sessionID string, opts EditOptions,
) (Turn, BranchPlan, error) {
	plan, err := s.editPlan(ctx, sessionID, opts)
	if err != nil {
		return Turn{}, BranchPlan{}, err
	}
	turn, err := s.startTurn(ctx, sessionID, opts.TurnID)
	if err != nil {
		return Turn{}, BranchPlan{}, err
	}
	return turn, plan, nil
}

// editPlan 校验消息并算出执行计划。
func (s *Service) editPlan(ctx context.Context, sessionID string, opts EditOptions) (BranchPlan, error) {
	text := strings.TrimSpace(opts.Text)
	if text == "" {
		return BranchPlan{}, fmt.Errorf("消息内容不能为空: %w", ErrInvalid)
	}

	orig, err := s.repo.GetMessage(ctx, opts.MessageID)
	if err != nil {
		return BranchPlan{}, err
	}
	if orig.SessionID != sessionID {
		return BranchPlan{}, fmt.Errorf("消息 %s 不属于会话 %s: %w", opts.MessageID, sessionID, ErrInvalid)
	}
	if orig.Role != llm.RoleUser {
		return BranchPlan{}, fmt.Errorf("只能编辑用户消息，消息 %s 的角色是 %s: %w", opts.MessageID, orig.Role, ErrInvalid)
	}

	// 新用户消息挂在原消息的父节点下，成为原消息的兄弟；
	// 上下文读到父节点为止，这样编辑后的消息看到的「前文」与原消息一致
	return BranchPlan{
		parentID: orig.ParentID,
		leafID:   orig.ParentID,
		userText: text,
	}, nil
}

// EditMessageWith 编辑并同步跑完新分支。
func (s *Service) EditMessageWith(
	ctx context.Context, sessionID string, opts EditOptions, cb StreamCallback, ask AskOptions,
) (TurnResult, error) {
	plan, err := s.editPlan(ctx, sessionID, opts)
	if err != nil {
		return TurnResult{}, err
	}
	turn, err := s.startTurn(ctx, sessionID, opts.TurnID)
	if err != nil {
		return TurnResult{}, err
	}
	return s.RunBranchTurn(ctx, sessionID, plan, turn.ID, cb, ask)
}

// RunBranchTurn 用已算好的计划执行一个分支轮次。
//
// Web 先 Regenerate/EditMessage 拿到 turn_id（HTTP 立刻返回），
// 再在后台用同一个 plan 跑——否则那两次消息查询要重做一遍。
func (s *Service) RunBranchTurn(
	ctx context.Context, sessionID string, plan BranchPlan, turnID string,
	cb StreamCallback, ask AskOptions,
) (TurnResult, error) {
	return s.AskWith(ctx, sessionID, plan.userText, cb, plan.apply(ask, turnID))
}

// apply 把计划写进 AskOptions。
func (p BranchPlan) apply(ask AskOptions, turnID string) AskOptions {
	ask.ParentID = p.parentID
	ask.LeafID = p.leafID
	ask.PreparedTurnID = turnID
	ask.SkipUserMessage = p.skipUserMessage
	return ask
}

// startTurn 创建一个 Running 状态的轮次并注册取消句柄。
//
// turnID 非空时复用调用方已建的轮次（Web 先 PrepareTurn 再跑）。
// PrepareTurn、Regenerate、EditMessage 三条路径共用这一处，
// 轮次语义变化时只需要改一个地方。
func (s *Service) startTurn(ctx context.Context, sessionID, turnID string) (Turn, error) {
	turn := Turn{
		ID:        turnID,
		SessionID: sessionID,
		Status:    TurnRunning,
		StartedAt: s.now(),
	}
	if turn.ID == "" {
		turn.ID = idgen.New()
		if err := s.repo.CreateTurn(ctx, turn); err != nil {
			return Turn{}, fmt.Errorf("创建轮次: %w", err)
		}
	}
	s.turns.reserve(turn.ID)
	return turn, nil
}
