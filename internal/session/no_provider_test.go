package session

import (
	"testing"

	"github.com/lincanyitse/baiyi-chat/internal/agent"
	"github.com/lincanyitse/baiyi-chat/internal/provider/registry"
)

// TestValidateSessionModelChecksDefaultRoute 锁定一个真实缺陷：
// ModelRef 为空时曾直接返回 nil，导致「一个 Provider 都没配」也能通过
// 提交预检，等后台选型才失败，用户看到的是一次卡住的轮次。
func TestValidateSessionModelChecksDefaultRoute(t *testing.T) {
	// 空的 registry：没有任何 provider，路由也指不到东西
	reg, err := registry.Build(emptyConfig())
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(Options{
		Registry: reg,
		Log:      discardLog(),
		Budget:   agent.Budget{},
	})

	err = svc.ValidateSessionModel(Session{ModelRef: ""})
	if err == nil {
		t.Fatal("没有可用 Provider 时，跟随默认路由也必须报错")
	}
}

// TestValidateSessionModelAcceptsConfiguredProvider 确认正常配置时不误报。
func TestValidateSessionModelAcceptsConfiguredProvider(t *testing.T) {
	reg, err := registry.Build(configuredConfig())
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(Options{
		Registry: reg,
		Log:      discardLog(),
		Budget:   agent.Budget{},
	})

	if err := svc.ValidateSessionModel(Session{ModelRef: ""}); err != nil {
		t.Fatalf("配了 Provider 时不该报错: %v", err)
	}
}
