package web

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// SystemInfoDTO 是「关于」页需要的信息。
//
// 这些是排障时最先被问到的：版本、数据在哪、日志在哪、配置在哪。
// 以前它们只存在于启动日志里，用户看不到。
type SystemInfoDTO struct {
	Version string `json:"version"`
	GoVer   string `json:"go_version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`

	UptimeSec int64 `json:"uptime_sec"`

	DataDir    string `json:"data_dir"`
	ConfigPath string `json:"config_path"`
	LogFile    string `json:"log_file"`
	DBPath     string `json:"db_path"`

	// DBBytes 是数据库文件大小，让用户对「数据有多重」有概念。
	DBBytes int64 `json:"db_bytes"`
	// Backups 是已有备份数。
	Backups int `json:"backups"`
}

// handleSystemInfo 返回系统信息。
func (s *Server) handleSystemInfo(w http.ResponseWriter, _ *http.Request) {
	out := SystemInfoDTO{
		Version:    s.opts.Version,
		GoVer:      runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		UptimeSec:  int64(time.Since(s.startAt).Seconds()),
		ConfigPath: s.opts.ConfigPath,
		LogFile:    s.opts.LogFile,
	}

	if s.opts.DataDir != "" {
		out.DataDir = s.opts.DataDir
		out.DBPath = filepath.Join(s.opts.DataDir, "baiyi.db")
		if info, err := os.Stat(out.DBPath); err == nil {
			out.DBBytes = info.Size()
		}
	}

	if s.opts.ListBackups != nil {
		if all, err := s.opts.ListBackups(); err == nil {
			out.Backups = len(all)
		}
	}

	writeJSON(w, http.StatusOK, out)
}
