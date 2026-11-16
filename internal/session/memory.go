package session

import (
	"context"
	"strings"

	"github.com/lincanyitse/baiyi-chat/internal/memory"
)

// MemoryHooks 把记忆子系统接进会话。
//
// 用接口而不是直接依赖 memory 的完整结构：session 只需要
// 「召回画像卡与情节」和「抽取并写入」两件事，收窄接口让测试
// 不必构造整个记忆库。
type MemoryHooks struct {
	Repo     *memory.Repo
	Recaller *memory.Recaller
	Writer   *memory.Writer
	Extract  *memory.Extractor
	Decayer  *memory.Decayer
	// Episodic 决定是否把本轮对话存成情节。
	Episodic bool
}

// RecallMemory 在组装上下文前取画像卡与情节。
//
// 失败一律降级为空：记忆是可选的增强，它出问题不该让对话失败。
func (s *Service) RecallMemory(ctx context.Context, userText string) (profile string, episodes []string) {
	if s.memory == nil || s.memory.Recaller == nil {
		return "", nil
	}
	p, e, err := s.memory.Recaller.Recall(ctx, userText)
	if err != nil {
		s.log.Warn("记忆召回失败，本轮不注入", "err", err)
		return "", nil
	}
	return p, e
}

// CaptureMemory 在助手回复后抽取候选并写入。
//
// 只抽取**用户说的内容**，不看助手回复——助手可能复述外部文档，
// 那部分不属于用户的长期信息。
func (s *Service) CaptureMemory(ctx context.Context, sessionID, turnID, userText, assistantText string) {
	if s.memory == nil || s.memory.Extract == nil || s.memory.Writer == nil || s.memory.Repo == nil {
		return
	}

	// 用不随请求取消的 ctx：用户中断对话不该丢掉已经说出口的记忆
	ctx = context.WithoutCancel(ctx)

	cands := s.memory.Extract.Extract(userText)
	for _, c := range cands {
		c.SessionID = sessionID
		c.TurnID = turnID
		if _, err := s.memory.Writer.Write(ctx, c); err != nil {
			s.log.Warn("写入记忆失败", "subject", c.Subject, "predicate", c.Predicate, "err", err)
		}
	}

	if s.memory.Episodic && assistantText != "" {
		summary := memory.SummarizeTurn(userText, assistantText)
		if summary == "" {
			return
		}
		if _, err := s.memory.Repo.InsertEpisode(ctx, memory.Episode{
			SessionID:  sessionID,
			TurnID:     turnID,
			Summary:    summary,
			Keywords:   memory.KeywordsOf(summary),
			Importance: episodeImportance(userText),
		}); err != nil {
			s.log.Warn("写入情节失败", "err", err)
		}
	}
}

// episodeImportance 给本轮对话估一个重要性。
//
// 简单启发：问题越长、越像「讨论」而不是「查一下」，越值得留存。
// 不做语义判断——误判的代价是召回时多一条噪声，很轻。
func episodeImportance(userText string) float64 {
	n := len([]rune(strings.TrimSpace(userText)))
	switch {
	case n >= 60:
		return 0.8
	case n >= 20:
		return 0.6
	default:
		return 0.4
	}
}
