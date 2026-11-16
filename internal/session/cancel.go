package session

import (
	"context"
	"sync"
)

// turnHandle 是进行中轮次的取消句柄。
type turnHandle struct {
	// cancel 为 nil 表示「已预占但还没开跑」——此时停止请求只置
	// canceled 标记，等 begin 时立刻取消。
	cancel  context.CancelFunc
	stopped bool
}

// turnRegistry 记录进行中的轮次，让「停止」能精确取消某一轮。
//
// 为什么需要它：渠道层只有一个请求 ctx，取消它会连带杀掉整个会话——
// REPL 的 Ctrl-C 曾经就是这样，按一次之后所有命令都失败。把取消权
// 收到「按 turn 注册」的粒度，才能做到「只停当前这一轮」。
type turnRegistry struct {
	mu      sync.Mutex
	handles map[string]*turnHandle
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{handles: make(map[string]*turnHandle)}
}

// reserve 预占一个轮次 ID，让「还没真正开跑」的轮次也能被停止。
//
// PrepareTurn 与 AskWith 之间有个窗口期（后台 goroutine 还没调度到），
// 这期间用户点停止必须被接受，否则停止按钮看起来「没反应」。
func (r *turnRegistry) reserve(turnID string) {
	r.mu.Lock()
	if _, ok := r.handles[turnID]; !ok {
		r.handles[turnID] = &turnHandle{}
	}
	r.mu.Unlock()
}

// begin 为 turnID 派生可取消的 ctx，返回 ctx 与结束函数。
//
// 结束函数必须在轮次结束时调用（defer），否则 map 会随会话一直增长。
func (r *turnRegistry) begin(parent context.Context, turnID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)

	r.mu.Lock()
	h, ok := r.handles[turnID]
	if !ok {
		h = &turnHandle{}
		r.handles[turnID] = h
	}
	h.cancel = cancel
	// 窗口期内已被要求停止：立刻取消，尊重用户意图
	alreadyStopped := h.stopped
	r.mu.Unlock()

	if alreadyStopped {
		cancel()
	}

	var once sync.Once
	done := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.handles, turnID)
			r.mu.Unlock()
			cancel()
		})
	}
	return ctx, done
}

// cancelTurn 取消指定轮次。返回是否找到。
func (r *turnRegistry) cancelTurn(turnID string) bool {
	r.mu.Lock()
	h, ok := r.handles[turnID]
	if !ok {
		r.mu.Unlock()
		return false
	}
	h.stopped = true
	cancel := h.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return true
}

// running 报告该轮次是否仍在进行。
func (r *turnRegistry) running(turnID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.handles[turnID]
	return ok
}

// CancelTurn 停止正在生成的一轮。
//
// 只取消该轮，不影响会话里的其他操作——这是 Web 停止按钮与 REPL
// Ctrl-C 的共同入口。
func (s *Service) CancelTurn(turnID string) bool {
	return s.turns.cancelTurn(turnID)
}

// IsTurnRunning 报告该轮是否仍在进行。供「停止」接口判断幂等性。
func (s *Service) IsTurnRunning(turnID string) bool {
	return s.turns.running(turnID)
}
