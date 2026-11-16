package bootstrap

import (
	"context"

	"github.com/lincanyitse/baiyi-chat/internal/channel/web"
)

// ResetPassword 清除本地口令，供 `baiyi reset-password` 使用。
//
// 这是在「忘了口令进不去」时唯一的自救路径：不需要先登录，
// 只要能在本机执行命令即可。
func (a *App) ResetPassword(_ context.Context) error {
	auth := web.NewAuth(a.DB.DB)
	return auth.Reset()
}
