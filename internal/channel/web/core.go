package web

// Web 对会话服务的依赖。按使用侧拆开，再收成 Core。
//
// *session.Service 隐式满足 Core，所以装配和现有测试不用改。
// 渠道仍引用 session 的数据类型（Turn、AskOptions 等）；这里去掉的是
// 对具体 Service 的依赖，测试可以注入只实现其中一组方法的假对象。

import (
	"context"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
	"github.com/lincanyitse/baiyi-chat/internal/session"
)

// SessionAPI 是会话、消息与分支树的读写。
type SessionAPI interface {
	GetSession(ctx context.Context, id string) (session.Session, error)
	CreateSession(ctx context.Context, title string) (session.Session, error)
	RenameSession(ctx context.Context, id, title string) error
	DeleteSession(ctx context.Context, id string) error
	ArchiveSession(ctx context.Context, id string, archived bool) error
	SearchSessions(ctx context.Context, q session.SessionQuery) (session.SessionPage, error)
	SearchMessages(ctx context.Context, sessionID, query string, limit int) ([]session.StoredMessage, error)
	GetMessage(ctx context.Context, id string) (session.StoredMessage, error)
	GetTurn(ctx context.Context, id string) (session.Turn, error)
	MessageTreeOf(ctx context.Context, sessionID, leaf string) (session.MessageTree, error)
	HistoryAt(ctx context.Context, sessionID, leaf string) ([]llm.Message, error)
	BranchOptions(ctx context.Context, sessionID, messageID string) ([]session.BranchOption, error)
}

// TurnAPI 是一轮对话的准备、执行、续跑与取消。
type TurnAPI interface {
	PrepareTurn(ctx context.Context, sessionID string) (session.Turn, error)
	AskTurn(ctx context.Context, sessionID, turnID, userText string, cb session.StreamCallback, opts session.AskOptions) (session.TurnResult, error)
	ResumeTurnWith(ctx context.Context, sessionID, turnID string, cb session.StreamCallback, opts session.AskOptions) (session.TurnResult, error)
	RunBranchTurn(ctx context.Context, sessionID string, plan session.BranchPlan, turnID string, cb session.StreamCallback, ask session.AskOptions) (session.TurnResult, error)
	EditMessage(ctx context.Context, sessionID string, opts session.EditOptions) (session.Turn, session.BranchPlan, error)
	Regenerate(ctx context.Context, sessionID string, opts session.RegenerateOptions) (session.Turn, session.BranchPlan, error)
	CancelTurn(turnID string) bool
	IsTurnRunning(turnID string) bool
}

// ModelAPI 是模型列表、会话级选择与能力查询。
type ModelAPI interface {
	ListModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error)
	ModelCapabilities(ref registry.ModelRef) llm.Capabilities
	SessionModel(sess session.Session) (registry.ModelRef, bool)
	SetSessionModel(ctx context.Context, sessionID, modelRef string) error
	ValidateSessionModel(sess session.Session) error
}

// ContextAPI 是上下文快照的读取，供检查器画表与对比。
type ContextAPI interface {
	ContextSnapshotAt(ctx context.Context, turnID string, step int) (session.ContextSnapshot, error)
	ContextSnapshotByID(ctx context.Context, id string) (session.ContextSnapshot, error)
	ListContextSnapshots(ctx context.Context, sessionID string, limit int) ([]session.ContextSnapshotMeta, error)
}

// Core 是 Web 渠道用到的全部会话能力。
type Core interface {
	SessionAPI
	TurnAPI
	ModelAPI
	ContextAPI
}

// 编译期确认具体服务满足渠道声明的方法。
var _ Core = (*session.Service)(nil)
