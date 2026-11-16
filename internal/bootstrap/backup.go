package bootstrap

// 备份与恢复。
//
// 备份用 SQLite 的 VACUUM INTO，不是文件复制：WAL 模式下最近的写入
// 可能还在 -wal 文件里，只复制 .db 会得到一个「看起来正常但少了最后
// 几轮对话」的备份——这比没有备份更危险，因为用户会以为数据保住了。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// BackupInfo 描述一个备份文件。
type BackupInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Backup 立即生成一份数据库快照，返回文件路径。
//
// 文件落在 <数据目录>/backups/，命名带时间戳，便于排序与人工识别。
func (a *App) Backup(ctx context.Context) (BackupInfo, error) {
	dir := filepath.Join(a.Config.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupInfo{}, fmt.Errorf("创建备份目录: %w", err)
	}

	name := fmt.Sprintf("baiyi-%s.db", time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	// VACUUM INTO 不接受已存在的目标文件
	if _, err := os.Stat(path); err == nil {
		return BackupInfo{}, fmt.Errorf("备份文件已存在: %s", path)
	}

	// VACUUM INTO 的路径要写成 SQL 字符串字面量。
	// 用参数占位符会报语法错（PRAGMA/VACUUM 都不支持绑定参数）。
	// 路径由服务端固定生成，不含用户输入，转义只是防御。
	quoted := "'" + escapeSQLString(path) + "'"
	if _, err := a.DB.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return BackupInfo{}, fmt.Errorf("生成备份失败: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("备份已生成但读取信息失败: %w", err)
	}
	a.Log.Info("数据库备份完成", "path", path, "size", info.Size())
	return BackupInfo{Path: path, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// ListBackups 返回已有备份，按时间倒序。
func (a *App) ListBackups() ([]BackupInfo, error) {
	dir := filepath.Join(a.Config.DataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]BackupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 只认我们生成的两种命名：迁移前的 .bak 与手动备份的 .db
		if !isBackupName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Path:    filepath.Join(dir, name),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

func isBackupName(name string) bool {
	switch {
	case filepath.Ext(name) == ".bak":
		return true
	case filepath.Ext(name) == ".db" && len(name) > 10:
		return true
	}
	return false
}

// escapeSQLString 转义 SQL 单引号字面量里的单引号。
func escapeSQLString(s string) string {
	out := make([]rune, 0, len(s)+4)
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
