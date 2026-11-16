// Package jobs 提供持久化的后台任务队列。
//
// 为什么需要它：嵌入 200 页语料、重建向量、生成摘要是分钟级操作，
// 不能卡在对话热路径上；进程崩溃后任务也不能丢。因此队列落在 SQLite，
// 而不是内存 channel。
//
// 依赖约束：只认 Handler 接口，不认任何具体业务。六个 handler 由
// bootstrap 注册，避免 jobs → rag/memory 的反向依赖。
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Status 是任务状态。
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Job 是一条待执行任务。
type Job struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	DedupeKey   string          `json:"dedupe_key,omitempty"`
	Status      Status          `json:"status"`
	Priority    int             `json:"priority"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	LastError   string          `json:"last_error,omitempty"`
	RunAfter    time.Time       `json:"run_after"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
}

// Handler 处理一种任务。
//
// 返回值语义：
//   - nil：成功，任务标记为 done
//   - ErrCancel：不可恢复，直接转 canceled，不重试
//   - 其他错误：按可重试处理；超过 MaxAttempts 转 failed
type Handler interface {
	Kind() string
	Handle(ctx context.Context, job Job) error
}

// ErrCancel 让任务直接失败且不重试。
//
// Handler 用它表示「重试多少次结果都一样」——资料文件被删了、
// 格式不支持、配置里根本没这个模型。
var ErrCancel = errors.New("jobs: 任务不可恢复")

// Decode 把 payload 解到 v。
func (j Job) Decode(v any) error {
	if len(j.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(j.Payload, v)
}

// Enqueue 是提交任务的参数。
type Enqueue struct {
	Kind    string
	Payload any
	// DedupeKey 非空且已有同 kind 同 key 的 pending/running 任务时，
	// 不重复插入，返回既有任务。
	DedupeKey   string
	Priority    int
	MaxAttempts int
	// RunAfter 为 nil 表示立即可执行。
	RunAfter *time.Time
}
