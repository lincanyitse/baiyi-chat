// 白泽 baiyi-chat —— 个人 AI 助理。
//
// 无参数启动直接进 Web 界面：这是产品的主入口。
// REPL 是调试工具，用 `baiyi repl` 显式进入。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lincanyitse/baiyi-chat/internal/bootstrap"
	"github.com/lincanyitse/baiyi-chat/internal/infra/browser"
	"github.com/lincanyitse/baiyi-chat/internal/infra/config"
)

// 由 Makefile 的 -ldflags 注入。
var (
	version = "dev"
	commit  = "none"
)

func main() {
	var (
		cfgPath = flag.String("config", "", "配置文件路径（默认 <数据目录>/config.toml）")
		showVer = flag.Bool("version", false, "显示版本")
		dataDir = flag.String("data-dir", "", "数据目录（覆盖配置与环境变量）")
		listen  = flag.String("listen", "", "覆盖 Web 监听地址，如 127.0.0.1:9000")
		noOpen  = flag.Bool("no-open", false, "启动时不自动打开浏览器")
		open    = flag.Bool("open", false, "强制打开浏览器（即使上次已开过）")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("baiyi %s (%s)\n", version, commit)
		return
	}

	if *dataDir != "" {
		_ = os.Setenv("BAIYI_DATA_DIR", *dataDir)
	} else if v := dataDirAfterSubcommand(); v != "" {
		// Go 的 flag 在遇到第一个非 flag 参数后停止解析，
		// 所以 `baiyi restore X --data-dir Y` 里的 --data-dir 不会被
		// flag.Parse 收走。这里补一次手动提取，让两种写法都成立。
		_ = os.Setenv("BAIYI_DATA_DIR", v)
	}

	path := *cfgPath
	if path == "" {
		path = filepath.Join(config.Default().DataDir, "config.toml")
	}

	// 子命令而不是 flag：flag 只能表达「开关」，表达不了「模式」。
	cmd := ""
	if len(flag.Args()) > 0 {
		cmd = flag.Arg(0)
	}
	// restore 必须在 Wire 之前：它要替换数据库文件，
	// 而 Wire 会打开数据库并加锁。
	if cmd == "restore" {
		backupPath := flag.Arg(1)
		if backupPath == "" {
			fmt.Fprintln(os.Stderr, "用法: baiyi restore <备份文件路径>")
			fmt.Fprintln(os.Stderr, "备份文件在 <数据目录>/backups/ 下，也可用界面「设置 → 数据」生成")
			os.Exit(2)
		}
		// 数据目录同样受 --data-dir 与 BAIYI_DATA_DIR 影响
		dataDir := config.Default().DataDir
		res, err := bootstrap.Restore(backupPath, dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "恢复失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("已恢复: %s\n", res.DBPath)
		if res.PreviousDB != "" {
			fmt.Printf("原数据库已保留: %s\n", res.PreviousDB)
			fmt.Println("确认无误后可以删除它；要回退就把两边的文件名换回来。")
		}
		return
	}

	ctx := context.Background()
	app, err := bootstrap.Wire(ctx, path)
	if err != nil {
		// logger 创建之后的失败已写入日志文件；之前的不可能（那时还没有 logger）。
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		if logFile := bootstrap.LogPathIfReady(err); logFile != "" {
			fmt.Fprintf(os.Stderr, "详见日志: %s\n", logFile)
		}
		os.Exit(1)
	}
	defer func() { _ = app.Close() }()
	app.Version = version

	if *listen != "" {
		app.Config.Web.Listen = *listen
	}

	// 浏览器打开策略：只在「第一次」或「地址变了」时打开。
	// 每次重启都弹新标签页会很烦，而端口变了时旧页已经连不上。
	openBrowser := !*noOpen
	lastURLFile := filepath.Join(app.Config.DataDir, "last-url")
	app.OnWebReady = func(url string) {
		fmt.Printf("\n  白泽已启动：%s\n  按 Ctrl+C 停止\n\n", url)
		if !openBrowser {
			return
		}
		if err := browser.MaybeOpen(lastURLFile, url, *open); err != nil {
			app.Log.Warn("打开浏览器失败，请手动访问上面的地址", "err", err)
		}
	}

	// 子命令而不是 flag：flag 只能表达「开关」，表达不了「模式」。
	// （cmd 已在 Wire 之前解析，见上）

	switch cmd {
	case "repl":
		// 调试入口。REPL 不再是无参数时的默认行为——
		// 真实用户拿到 exe 双击，期望看到的是界面。
		if err := app.RunREPL(ctx); err != nil {
			app.Log.Error("REPL 异常退出", "err", err)
			fmt.Fprintf(os.Stderr, "运行结束: %v\n", err)
			os.Exit(1)
		}
		return
	case "reset-password":
		// 忘记口令时的唯一出口。直接清空 web_auth，下次打开 Web
		// 会重新进入「设置口令」流程。
		if err := app.ResetPassword(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "重置口令失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("口令已清除。下次打开 Web 界面时会要求重新设置。")
		return
	case "serve", "":
		// 两者等价：`serve` 保留给显式指定的场景（脚本、文档），
		// 无参数是双击与日常使用的默认路径。
		if err := app.RunWeb(ctx); err != nil {
			app.Log.Error("Web 服务异常退出", "err", err)
			fmt.Fprintf(os.Stderr, "运行结束: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "未知命令 %q。可用：serve（默认）/ repl / reset-password / restore\n", cmd)
	os.Exit(2)
}

// dataDirAfterSubcommand 从子命令之后的参数里提取 --data-dir。
//
// Go 的 flag 包在遇到第一个非 flag 参数后停止解析，所以
// `baiyi restore X --data-dir Y` 里的 --data-dir 会被当成位置参数。
// 支持两种写法（--data-dir Y 与 --data-dir=Y），避免让用户记顺序。
func dataDirAfterSubcommand() string {
	args := flag.Args()
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--data-dir" && i+1 < len(args):
			return args[i+1]
		case len(args[i]) > len("--data-dir=") && args[i][:len("--data-dir=")] == "--data-dir=":
			return args[i][len("--data-dir="):]
		}
	}
	return ""
}
