package session

import (
	"context"
	"fmt"
)

// GetSession 按 ID 取会话。
func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	return s.repo.GetSession(ctx, id)
}

// GetTurn 按 ID 取轮次。
func (s *Service) GetTurn(ctx context.Context, id string) (Turn, error) {
	return s.repo.GetTurn(ctx, id)
}

// GetMessage 按 ID 取一条消息。
func (s *Service) GetMessage(ctx context.Context, id string) (StoredMessage, error) {
	return s.repo.GetMessage(ctx, id)
}

// PrepareTurn 先创建一个待执行的轮次并返回，让调用方能立刻拿到 turnID。
//
// 为什么需要它：Web 要先把 turn_id 返回给前端（用于订阅 SSE 与停止），
// 再在后台跑完整轮。AskWith 是阻塞的，它内部会自己建 turn——这里
// 预先建好并让 AskTurn 复用它，避免出现两个 turn。
//
// 同时注册取消句柄：从 PrepareTurn 返回到 AskWith 真正开跑之间有个
// 窗口期，如果这期间用户点停止，IsTurnRunning 必须已经为真。
func (s *Service) PrepareTurn(ctx context.Context, sessionID string) (Turn, error) {
	return s.startTurn(ctx, sessionID, "")
}

// AskTurn 执行一个已由 PrepareTurn 创建的轮次。
//
// 与 AskWith 的区别：轮次已存在，跳过创建步骤。其余逻辑完全一致——
// 不给 Web 单独开一条执行路径，否则两边的取消、审批、落库会慢慢分叉。
func (s *Service) AskTurn(
	ctx context.Context, sessionID, turnID, userText string,
	cb StreamCallback, opts AskOptions,
) (TurnResult, error) {
	opts.PreparedTurnID = turnID
	return s.AskWith(ctx, sessionID, userText, cb, opts)
}

// ResumeTurnWith 续跑并透传审批设置。
//
// 早先的 ResumeTurn 不传 Approver，导致续跑路径上的写类与危险工具
// 拿不到审批器——Web 端续跑会直接跳过确认。
func (s *Service) ResumeTurnWith(
	ctx context.Context, sessionID, turnID string,
	cb StreamCallback, opts AskOptions,
) (TurnResult, error) {
	turn, err := s.repo.GetTurn(ctx, turnID)
	if err != nil {
		return TurnResult{}, err
	}
	if turn.Status != TurnRunning && turn.Status != TurnAwaiting {
		return TurnResult{}, fmt.Errorf("轮次 %s 状态为 %s，无需恢复", turnID, turn.Status)
	}

	history, err := s.HistoryAt(ctx, sessionID, opts.LeafID)
	if err != nil {
		return TurnResult{}, err
	}
	userText := lastUserText(history)
	if userText == "" {
		return TurnResult{}, fmt.Errorf("轮次 %s 找不到对应的用户输入", turnID)
	}

	// 把状态改回 running，然后复用原 turn 续跑。
	// AskWith 会通过 tool_results 缓存跳过已完成的工具调用，不会重复执行。
	if err := s.repo.UpdateTurnStatus(ctx, turnID, TurnRunning, "", nil); err != nil {
		return TurnResult{}, err
	}
	s.log.Info("恢复轮次", "turn", turnID, "session", sessionID)

	opts.ResumeTurnID = turnID
	return s.AskWith(ctx, sessionID, userText, cb, opts)
}
