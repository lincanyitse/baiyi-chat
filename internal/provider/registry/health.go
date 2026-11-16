package registry

import (
	"sync"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// 健康度阈值与冷却时长。
const (
	// failuresBeforeCooldown 是连续失败多少次后进入冷却。
	failuresBeforeCooldown = 3
	// cooldownDuration 是冷却时长：期间该 Provider 被跳过。
	cooldownDuration = 60 * time.Second
)

// health 记录单个 Provider 的健康状态。
type health struct {
	consecutiveFailures int
	cooldownUntil       time.Time
	lastErr             string
}

// healthTracker 管理全部 Provider 的健康度。
type healthTracker struct {
	mu     sync.Mutex
	states map[string]*health
	now    func() time.Time
}

func newHealthTracker() *healthTracker {
	return &healthTracker{states: map[string]*health{}, now: time.Now}
}

// available 报告某 Provider 当前是否可用（不在冷却期）。
func (h *healthTracker) available(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	st, ok := h.states[name]
	if !ok {
		return true
	}
	return !h.now().Before(st.cooldownUntil)
}

// recordSuccess 清零失败计数。
func (h *healthTracker) recordSuccess(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.states[name]; ok {
		st.consecutiveFailures = 0
		st.cooldownUntil = time.Time{}
		st.lastErr = ""
	}
}

// recordFailure 累加失败；达到阈值进入冷却。
func (h *healthTracker) recordFailure(name string, kind llm.ErrorKind, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st, ok := h.states[name]
	if !ok {
		st = &health{}
		h.states[name] = st
	}
	st.consecutiveFailures++
	st.lastErr = msg

	// 密钥或配额问题不是瞬时故障，重试无意义，直接冷却
	if kind == llm.ErrAuth || kind == llm.ErrQuota ||
		st.consecutiveFailures >= failuresBeforeCooldown {
		st.cooldownUntil = h.now().Add(cooldownDuration)
	}
}

// status 返回某 Provider 的健康摘要，供诊断展示。
func (h *healthTracker) status(name string) (failures int, cooling bool, lastErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.states[name]
	if !ok {
		return 0, false, ""
	}
	return st.consecutiveFailures, h.now().Before(st.cooldownUntil), st.lastErr
}

// Candidate 是一个候选模型及其 Provider 实例。
type Candidate struct {
	Provider llm.Provider
	Ref      ModelRef
}

// ResolveChain 返回某任务类型下**全部**可用候选，按配置顺序排列。
//
// 与 Resolve 的区别：Resolve 只取第一个，ResolveChain 让调用方
// 在遇到可重试错误时逐个尝试——这是 FR-PRV-008 的降级链基础。
func (r *Registry) ResolveChain(task string) []Candidate {
	var out []Candidate
	for _, ref := range r.Candidates(task) {
		p, ok := r.providers[ref.Provider]
		if !ok {
			continue
		}
		if !r.health.available(ref.Provider) {
			continue // 冷却中的跳过
		}
		out = append(out, Candidate{Provider: p, Ref: ref})
	}
	return out
}

// RecordResult 记录一次调用的结果，用于健康度统计。
func (r *Registry) RecordResult(providerName string, err *llm.Error) {
	if err == nil {
		r.health.recordSuccess(providerName)
		return
	}
	if err.Kind == llm.ErrCanceled {
		return // 用户取消不是 Provider 的锅
	}
	r.health.recordFailure(providerName, err.Kind, err.Message)
}

// HealthReport 返回全部 Provider 的健康摘要，供 /health 命令展示。
type HealthReport struct {
	Name     string
	Failures int
	Cooling  bool
	LastErr  string
}

// Health 返回健康报告。
func (r *Registry) Health() []HealthReport {
	out := make([]HealthReport, 0, len(r.providers))
	for name := range r.providers {
		failures, cooling, lastErr := r.health.status(name)
		out = append(out, HealthReport{
			Name: name, Failures: failures, Cooling: cooling, LastErr: lastErr,
		})
	}
	return out
}

// Fallbackable 报告该错误是否应该换下一个候选。
