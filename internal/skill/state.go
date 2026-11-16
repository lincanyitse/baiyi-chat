package skill

// 技能启停的持久化。
//
// 存 JSON 而不是写回 config.toml：技能是「用户数据」，配置是「系统配置」。
// 混在一个文件里，用户备份技能目录时配置会跟着漂移；而 TOML 的外科式
// 写回也要多维护一段，出错面变大。
//
// 文件不存在、损坏、或写着不存在的技能名，都按「全部启用」处理——
// 这个文件只表达「用户额外关掉了哪些」，丢了不该让技能消失。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateFileName = "skills.json"

// DefaultStatePath 返回技能启停状态的默认位置。
//
// 放在技能目录的**父目录**而不是技能目录里：用户分享或备份 skills/
// 时不该把「我关掉了哪些」一起带出去。
func DefaultStatePath(skillsRoot string) string {
	return filepath.Join(filepath.Dir(skillsRoot), stateFileName)
}

// state 是 skills.json 的结构。
type state struct {
	// Disabled 是用户显式禁用的技能名。
	Disabled []string `json:"disabled"`
}

// loadState 读取启停状态。任何读取或解析错误都退回空状态。
func loadState(path string) state {
	raw, err := os.ReadFile(path)
	if err != nil {
		return state{}
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return state{}
	}
	return st
}

// saveState 写回启停状态。
//
// 先写临时文件再改名：写到一半崩溃不会留下半截 JSON，
// 而下一次读取会把半截 JSON 当成「全部启用」——静默丢失用户的设置。
func saveState(path string, st state) error {
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("编码技能状态: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("写入技能状态: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换技能状态: %w", err)
	}
	return nil
}
