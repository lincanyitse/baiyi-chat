package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// distFS 是前端构建产物。
//
// Vite 的 build.outDir 指向本目录（go:embed 不能引用包目录之外的路径）。
// dist 未构建时只有一个 .gitkeep，embed 仍能编译——运行时会给出提示页。
//
//go:embed all:dist
var distFS embed.FS

// Static 返回前端静态资源。
//
// 返回的 http.FileSystem 根是 dist 子目录，调用方不必关心路径前缀。
func Static() (http.FileSystem, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		// 尚未构建前端：只提供 API
		return nil, false
	}
	return http.FS(sub), true
}
