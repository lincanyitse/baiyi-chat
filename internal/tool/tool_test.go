package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeTool struct {
	spec   Spec
	result Result
	err    error
	calls  int
}

func (f *fakeTool) Spec() Spec { return f.spec }

func (f *fakeTool) Execute(context.Context, json.RawMessage, *ExecContext) (Result, error) {
	f.calls++
	return f.result, f.err
}

func newFake(name string) *fakeTool {
	return &fakeTool{
		spec:   DefaultSpec(Spec{Name: name, Description: "测试工具", Risk: RiskRead}),
		result: TextResult("ok"),
	}
}

func TestRegistryRegisterAndSpecs(t *testing.T) {
	r := NewRegistry()
	a, b := newFake("alpha"), newFake("beta")
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}

	specs := r.Specs()
	if len(specs) != 2 {
		t.Fatalf("spec 数 = %d", len(specs))
	}
	// 顺序应与注册顺序一致，保证下发给模型的 schema 稳定
	if specs[0].Name != "alpha" || specs[1].Name != "beta" {
		t.Errorf("顺序 = %s,%s", specs[0].Name, specs[1].Name)
	}
}

func TestRegistryDisableExcludesFromSpecs(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register(newFake("a")))
	must(t, r.Register(newFake("b")))

	if err := r.SetEnabled("a", false); err != nil {
		t.Fatal(err)
	}

	specs := r.Specs()
	if len(specs) != 1 || specs[0].Name != "b" {
		t.Errorf("禁用工具不应出现在 specs 中: %+v", specs)
	}
	if _, ok := r.Get("a"); ok {
		t.Error("禁用工具不应能取到")
	}
}

func TestRegistryEnabledExcludesDisabled(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register(newFake("a")))
	must(t, r.Register(newFake("b")))
	must(t, r.SetEnabled("a", false))

	enabled := r.Enabled()
	if len(enabled) != 1 || enabled[0].Spec().Name != "b" {
		t.Errorf("Enabled() 应只含已启用工具: %+v", enabled)
	}
	// All 仍要能看到被禁用的工具，管理界面靠它展示全量
	if all := r.All(); len(all) != 2 {
		t.Errorf("All() 应含禁用的工具，得到 %d 个", len(all))
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register(newFake("a")))
	must(t, r.Register(newFake("b")))
	r.Unregister("a")

	names := r.Names()
	if len(names) != 1 || names[0] != "b" {
		t.Errorf("names = %v", names)
	}
}

func TestRegistryRejectsInvalidName(t *testing.T) {
	r := NewRegistry()
	bad := newFake("has space")
	if err := r.Register(bad); err == nil {
		t.Fatal("含空格的工具名应被拒绝")
	}
}

func TestValidateSpec(t *testing.T) {
	valid := DefaultSpec(Spec{Name: "ok_name-1"})
	if err := ValidateSpec(valid); err != nil {
		t.Errorf("合法名被拒绝: %v", err)
	}

	for _, name := range []string{"", "has space", "有中文", strings.Repeat("a", 65)} {
		if err := ValidateSpec(DefaultSpec(Spec{Name: name})); err == nil {
			t.Errorf("非法名 %q 应被拒绝", name)
		}
	}
}

func TestRegistryOverwriteKeepsOrder(t *testing.T) {
	r := NewRegistry()
	must(t, r.Register(newFake("a")))
	must(t, r.Register(newFake("b")))
	must(t, r.Register(newFake("a"))) // 覆盖

	if names := r.Names(); len(names) != 2 || names[0] != "a" {
		t.Errorf("覆盖后顺序 = %v", names)
	}
}

func TestTruncateNoOp(t *testing.T) {
	content := "短内容"
	tr := TruncateResult(content, TruncateOptions{})
	if tr.DidTruncate {
		t.Error("短内容不应截断")
	}
	if tr.Text != content {
		t.Errorf("text = %q", tr.Text)
	}
}

func TestTruncateByBytes(t *testing.T) {
	content := strings.Repeat("a", 40*1024)
	tr := TruncateResult(content, TruncateOptions{MaxBytes: 1024, MaxTokens: 1_000_000})

	if !tr.DidTruncate {
		t.Fatal("超过字节上限应截断")
	}
	if len(tr.Text) > 2048 {
		t.Errorf("截断后过长: %d 字节", len(tr.Text))
	}
	if !strings.Contains(tr.Text, "tool_output_read") {
		t.Error("截断提示应提到 tool_output_read")
	}
	if tr.Full != content {
		t.Error("Full 应保留原文")
	}
}

func TestTruncateByTokens(t *testing.T) {
	content := strings.Repeat("中文内容", 3000)
	tr := TruncateResult(content, TruncateOptions{MaxTokens: 100, MaxBytes: 10 * 1024 * 1024})

	if !tr.DidTruncate {
		t.Fatal("超过 token 上限应截断")
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	content := strings.Repeat("中文", 10000)
	tr := TruncateResult(content, TruncateOptions{MaxBytes: 101, MaxTokens: 1_000_000})

	if !utf8.ValidString(tr.Text) {
		t.Fatal("截断产生了非法 UTF-8")
	}
	// 截断处不能出现替换字符
	if strings.ContainsRune(tr.Text, '�') {
		t.Error("截断产生了替换字符")
	}
}

func TestApplyTruncationIncludesHandle(t *testing.T) {
	tr := TruncateResult(strings.Repeat("x", 5000), TruncateOptions{MaxBytes: 100})
	res := ApplyTruncation(tr, nil, "abc123")
	if !strings.Contains(res.Text(), "abc123") {
		t.Error("结果应包含句柄")
	}
}

func TestResultHelpers(t *testing.T) {
	ok := TextResult("成功")
	if ok.IsError || ok.Text() != "成功" {
		t.Errorf("TextResult = %+v", ok)
	}

	bad := ErrorResult("失败")
	if !bad.IsError || bad.Text() != "失败" {
		t.Errorf("ErrorResult = %+v", bad)
	}
}

func TestTaintState(t *testing.T) {
	var nilState *TaintState
	if nilState.IsTainted() {
		t.Error("nil TaintState 不应被视为污染")
	}
	nilState.MarkExternal("x") // 不应 panic

	ts := &TaintState{}
	if ts.IsTainted() {
		t.Error("初始不应污染")
	}
	ts.MarkExternal("web_fetch")
	if !ts.IsTainted() {
		t.Error("标记后应污染")
	}
}

func TestExecContextNow(t *testing.T) {
	fixed := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	ec := &ExecContext{Clock: func() time.Time { return fixed }}
	if !ec.Now().Equal(fixed) {
		t.Error("应使用注入的时钟")
	}

	var nilCtx *ExecContext
	if nilCtx.Now().IsZero() {
		t.Error("nil ExecContext 也应返回当前时间")
	}
}

func TestApprovalDecisionApproved(t *testing.T) {
	cases := map[ApprovalAction]bool{
		ApproveOnce: true, ApproveSession: true, ApproveForever: true,
		Deny: false, "": false,
	}
	for action, want := range cases {
		if got := (ApprovalDecision{Action: action}).Approved(); got != want {
			t.Errorf("%q → %v, 期望 %v", action, got, want)
		}
	}
}

func TestRiskLevelString(t *testing.T) {
	if RiskRead.String() != "read" || RiskDangerous.String() != "dangerous" {
		t.Error("风险等级字符串不正确")
	}
}

func TestDefaultSpecFillsValues(t *testing.T) {
	s := DefaultSpec(Spec{Name: "x"})
	if s.Timeout <= 0 || len(s.InputSchema) == 0 {
		t.Errorf("默认值未填充: %+v", s)
	}
}

func TestFakeToolErrorPropagates(t *testing.T) {
	f := &fakeTool{
		spec: DefaultSpec(Spec{Name: "boom"}),
		err:  errors.New("执行失败"),
	}
	_, err := f.Execute(context.Background(), nil, &ExecContext{})
	if err == nil {
		t.Error("工具错误应传播")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
