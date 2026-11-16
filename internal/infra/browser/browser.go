// Package browser 用系统默认浏览器打开网址。
//
// 不引入第三方库：各平台的打开方式都很简单，一个 os/exec 就够。
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Open 用系统默认浏览器打开 url。
//
// 失败不算严重错误：界面地址已经打印在终端里，用户可以自己点。
func Open(url string) error {
	switch runtime.GOOS {
	case "windows":
		// rundll32 是 Windows 自带，且不会像 `cmd /c start` 那样闪黑框
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		// Linux/BSD：xdg-open 是桌面环境的事实标准
		return exec.Command("xdg-open", url).Start()
	}
}

// shouldOpen 判断这次启动要不要自动打开浏览器。
//
// 只在「第一次」或「访问地址变了」时打开：反复重启不该堆积一堆
// 指向同一页面的标签页，而端口变了时旧标签页已经连不上了，
// 必须开新的。
func shouldOpen(stateFile, url string) bool {
	prev, err := os.ReadFile(stateFile)
	if err != nil {
		return true // 没有记录 = 第一次
	}
	return string(prev) != url
}

// remember 记下本次的访问地址，供下次判断。
func remember(stateFile, url string) {
	_ = os.WriteFile(stateFile, []byte(url), 0o600)
}

// MaybeOpen 按需打开浏览器，并记下状态。
//
// force 为真时无条件打开（`--open` 显式要求）。
func MaybeOpen(stateFile, url string, force bool) error {
	if !force && !shouldOpen(stateFile, url) {
		return nil
	}
	remember(stateFile, url)
	if err := Open(url); err != nil {
		return fmt.Errorf("打开浏览器失败: %w", err)
	}
	return nil
}
