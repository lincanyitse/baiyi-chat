package web

import (
	"context"
	"testing"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

func approvalReq(turnID, name string, risk tool.RiskLevel) tool.ApprovalRequest {
	return tool.ApprovalRequest{
		TurnID:    turnID,
		SessionID: "s1",
		Calls: []tool.ApprovalCall{{
			CallID:  "c1",
			Name:    name,
			Args:    []byte(`{"x":1}`),
			Risk:    risk,
			Summary: "删除文件",
		}},
		ExpiresAt: time.Now().Add(5 * time.Second),
	}
}

func TestBrokerApproveUnblocksRequest(t *testing.T) {
	b := NewApprovalBroker(nil)

	done := make(chan tool.ApprovalDecision, 1)
	go func() {
		d, _ := b.Request(context.Background(), approvalReq("t1", "rm", tool.RiskDangerous))
		done <- d
	}()

	// 等审批出现在挂起表里
	deadline := time.Now().Add(2 * time.Second)
	var id string
	for time.Now().Before(deadline) {
		if list := b.Pending(); len(list) > 0 {
			id = list[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("审批未出现在挂起表")
	}

	if !b.Decide(id, tool.ApprovalDecision{Action: tool.ApproveOnce}) {
		t.Fatal("Decide 应返回 true")
	}

	select {
	case d := <-done:
		if d.Action != tool.ApproveOnce {
			t.Errorf("决定 = %s，期望 once", d.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("审批未解除阻塞")
	}

	// 处理完后挂起表应为空
	if n := len(b.Pending()); n != 0 {
		t.Errorf("挂起表应清空，剩 %d 条", n)
	}
}

func TestBrokerTimeoutDenies(t *testing.T) {
	b := NewApprovalBroker(nil)

	req := approvalReq("t1", "rm", tool.RiskDangerous)
	req.ExpiresAt = time.Now().Add(80 * time.Millisecond)

	d, err := b.Request(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != tool.Deny {
		t.Errorf("超时应拒绝，得到 %s", d.Action)
	}
	if d.Reason == "" {
		t.Error("超时拒绝应带原因")
	}
}

func TestBrokerContextCancelDenies(t *testing.T) {
	b := NewApprovalBroker(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan tool.ApprovalDecision, 1)
	go func() {
		req := approvalReq("t1", "rm", tool.RiskDangerous)
		req.ExpiresAt = time.Now().Add(10 * time.Second)
		d, _ := b.Request(ctx, req)
		done <- d
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case d := <-done:
		if d.Action != tool.Deny {
			t.Errorf("取消应拒绝，得到 %s", d.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消未解除阻塞")
	}
}

func TestBrokerCancelTurnDeniesOnlyThatTurn(t *testing.T) {
	b := NewApprovalBroker(nil)

	results := make(chan struct {
		turn string
		d    tool.ApprovalDecision
	}, 2)
	for _, turn := range []string{"t1", "t2"} {
		go func(turn string) {
			req := approvalReq(turn, "rm", tool.RiskDangerous)
			req.ExpiresAt = time.Now().Add(3 * time.Second)
			d, _ := b.Request(context.Background(), req)
			results <- struct {
				turn string
				d    tool.ApprovalDecision
			}{turn, d}
		}(turn)
	}

	// 等两条都挂上
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.Pending()) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(b.Pending()); n != 2 {
		t.Fatalf("应有 2 条挂起，得到 %d", n)
	}

	b.CancelTurn("t1")

	// t1 应被拒，t2 仍在挂起
	seen := map[string]tool.ApprovalDecision{}
	for range 1 {
		select {
		case r := <-results:
			seen[r.turn] = r.d
		case <-time.After(2 * time.Second):
			t.Fatal("t1 未被取消")
		}
	}
	if seen["t1"].Action != tool.Deny {
		t.Errorf("t1 应被拒绝，得到 %s", seen["t1"].Action)
	}
	if n := len(b.Pending()); n != 1 {
		t.Errorf("t2 应仍在挂起，剩 %d 条", n)
	}

	b.CancelTurn("t2")
	<-results
}

func TestBrokerDecideUnknownReturnsFalse(t *testing.T) {
	b := NewApprovalBroker(nil)
	if b.Decide("nope", tool.ApprovalDecision{Action: tool.ApproveOnce}) {
		t.Error("未知审批应返回 false")
	}
}

func TestBrokerNotifyFiresOnNewRequest(t *testing.T) {
	b := NewApprovalBroker(nil)

	ch := b.Notify()
	go func() {
		req := approvalReq("t1", "rm", tool.RiskDangerous)
		req.ExpiresAt = time.Now().Add(300 * time.Millisecond)
		_, _ = b.Request(context.Background(), req)
	}()

	select {
	case <-ch:
		// 正常：新审批出现时唤醒
	case <-time.After(2 * time.Second):
		t.Fatal("新审批未唤醒等待者")
	}
}

func TestBrokerSessionApproval(t *testing.T) {
	b := NewApprovalBroker(nil)

	if b.IsApproved("file_write") {
		t.Fatal("初始状态不应有已放行的工具")
	}

	// 「允许一次」不记入会话授权
	b.MarkApproved("file_write", tool.ApproveOnce)
	if b.IsApproved("file_write") {
		t.Error("once 不应记入会话授权")
	}

	// 「拒绝」也不记入——否则误点会让工具整个会话不可用
	b.MarkApproved("file_write", tool.Deny)
	if b.IsApproved("file_write") {
		t.Error("deny 不应记入会话授权")
	}

	// 「本会话允许」才记录下来
	b.MarkApproved("file_write", tool.ApproveSession)
	if !b.IsApproved("file_write") {
		t.Error("session 应记入会话授权")
	}

	// 授权只对具体工具生效
	if b.IsApproved("other_tool") {
		t.Error("授权不应外溢到其他工具")
	}
}
