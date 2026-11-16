package bootstrap

// 从备份恢复数据库。
//
// 不在界面里做：恢复要先关数据库、替换文件、重启进程，
// 在运行中的进程里做等于自杀；而恢复错了会覆盖现有数据，
// 需要极强的二次确认，命令行更合适。
//
// 这个函数在 Wire 之前调用——那时数据库还没打开。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// RestoreResult 是一次恢复的结果。
type RestoreResult struct {
	// DBPath 是恢复后的数据库位置。
	DBPath string
	// PreviousDB 是原数据库改名后的路径（不存在时为空）。
	PreviousDB string
}

// Restore 用备份文件替换数据目录里的数据库。
//
// 顺序：先校验备份是完好的 SQLite 库，再把当前库改名保留，最后复制。
// 任何一步失败都不会破坏现有数据——最坏情况是什么都没变。
func Restore(backupPath, dataDir string) (RestoreResult, error) {
	if _, err := os.Stat(backupPath); err != nil {
		return RestoreResult{}, fmt.Errorf("备份文件不存在: %w", err)
	}

	// 校验：能在只读模式打开且 integrity_check 通过，才继续。
	if err := verifySQLite(backupPath); err != nil {
		return RestoreResult{}, fmt.Errorf("备份文件校验失败: %w", err)
	}

	target := filepath.Join(dataDir, "baiyi.db")

	var result RestoreResult
	result.DBPath = target

	// 把当前库改名保留，而不是删掉：恢复错了还能换回来。
	if _, err := os.Stat(target); err == nil {
		prev := fmt.Sprintf("%s.before-restore-%s", target, time.Now().Format("20060102-150405"))
		if err := os.Rename(target, prev); err != nil {
			return RestoreResult{}, fmt.Errorf("保留原数据库失败: %w", err)
		}
		result.PreviousDB = prev

		// WAL 模式下最近的写入可能还在 -wal 里，只留 .db 会得到一个
		// 少了最后几轮对话的「备份」——回退时同样救不回来。
		// 一并改名，让保留的那份是完整的。
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(target + suffix); err == nil {
				if err := os.Rename(target+suffix, prev+suffix); err != nil {
					return RestoreResult{}, fmt.Errorf("保留 %s 失败: %w", suffix, err)
				}
			}
		}
	}

	if err := copyFile(backupPath, target); err != nil {
		// 复制失败时把原库换回来
		if result.PreviousDB != "" {
			_ = os.Rename(result.PreviousDB, target)
		}
		return RestoreResult{}, fmt.Errorf("复制备份失败: %w", err)
	}

	return result, nil
}

// verifySQLite 用只读连接做一次完整性检查。
func verifySQLite(path string) error {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	var verdict string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&verdict); err != nil {
		return fmt.Errorf("不是有效的 SQLite 数据库: %w", err)
	}
	if verdict != "ok" {
		return fmt.Errorf("完整性检查未通过: %s", verdict)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
