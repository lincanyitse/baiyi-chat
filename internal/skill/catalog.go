package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Catalog 是技能目录：扫描目录、热加载、按需取用。
type Catalog struct {
	root string
	// statePath 是启停状态的落盘位置。空表示不持久化（测试用）。
	statePath string

	mu     sync.RWMutex
	skills map[string]*Skill
	// fingerprint 是目录内容的哈希，用于判断是否需要重新扫描。
	fingerprint string
	// disabled 是用户显式禁用的技能名。
	disabled map[string]bool
}

// NewCatalog 构造目录扫描器。root 不存在时视为空目录。
//
// 启停状态从 DefaultStatePath(root) 读取：用户关掉的技能在重启后
// 仍然是关掉的，否则「禁用」只是个装饰。
func NewCatalog(root string) *Catalog {
	statePath := DefaultStatePath(root)
	st := loadState(statePath)

	disabled := make(map[string]bool, len(st.Disabled))
	for _, name := range st.Disabled {
		disabled[name] = true
	}

	return &Catalog{
		root:      root,
		statePath: statePath,
		skills:    map[string]*Skill{},
		disabled:  disabled,
	}
}

// Root 返回技能根目录。
func (c *Catalog) Root() string { return c.root }

// Refresh 扫描目录，只在内容变化时重建索引。
//
// 不用 fsnotify：Windows 上文件监听不稳，而每次组装前做一次
// 目录内容哈希比对足够便宜（技能通常只有几十个）。
func (c *Catalog) Refresh() (changed bool, err error) {
	fp, err := c.computeFingerprint()
	if err != nil {
		return false, err
	}

	c.mu.RLock()
	unchanged := fp == c.fingerprint && c.fingerprint != ""
	c.mu.RUnlock()
	if unchanged {
		return false, nil
	}

	loaded, err := c.scan()
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.skills = loaded
	c.fingerprint = fp
	c.mu.Unlock()
	return true, nil
}

// ForceReload 强制重新扫描（供 /skills reload 使用）。
func (c *Catalog) ForceReload() error {
	loaded, err := c.scan()
	if err != nil {
		return err
	}
	fp, err := c.computeFingerprint()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.skills = loaded
	c.fingerprint = fp
	c.mu.Unlock()
	return nil
}

// scan 读取 root 下每个子目录的 SKILL.md。
func (c *Catalog) scan() (map[string]*Skill, error) {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*Skill{}, nil
		}
		return nil, err
	}

	out := make(map[string]*Skill, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(c.root, e.Name())
		s, err := Load(dir)
		if err != nil {
			// 单个技能坏了不应拖垮整批——跳过并继续
			continue
		}
		out[s.Spec.Name] = s
	}
	return out, nil
}

// computeFingerprint 汇总所有 SKILL.md 的内容哈希。
func (c *Catalog) computeFingerprint() (string, error) {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		path := filepath.Join(c.root, name, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(hashString(string(data)))
		b.WriteByte('\n')
	}
	return hashString(b.String()), nil
}

// Get 按名取技能（已禁用的不可取）。
func (c *Catalog) Get(name string) (*Skill, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.disabled[name] {
		return nil, false
	}
	s, ok := c.skills[name]
	return s, ok
}

// All 返回全部启用的技能，按名字排序。
func (c *Catalog) All() []*Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*Skill, 0, len(c.skills))
	for name, s := range c.skills {
		if c.disabled[name] {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}

// AllForAdmin 返回全部技能（含已禁用的），按名字排序。
//
// 管理界面需要看到被禁用的技能才能重新启用；All() 是给模型看的，
// 两者不能混用——把禁用技能下发给模型等于禁用失效。
func (c *Catalog) AllForAdmin() []*Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*Skill, 0, len(c.skills))
	for _, s := range c.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}

// Digests 返回 L0 目录：名字 + 一句话，供系统提示使用。
func (c *Catalog) Digests() []Digest {
	all := c.All()
	out := make([]Digest, 0, len(all))
	for _, s := range all {
		out = append(out, s.Digest())
	}
	return out
}

// MatchTriggers 返回命中确定性触发词的技能名。
func (c *Catalog) MatchTriggers(text string) []string {
	var out []string
	for _, s := range c.All() {
		if s.MatchesTrigger(text) {
			out = append(out, s.Spec.Name)
		}
	}
	return out
}

// SetEnabled 启用或禁用技能，并落盘。
//
// 落盘失败时回滚内存状态：否则界面显示「已禁用」、重启后又是启用，
// 用户会以为程序在骗他。
func (c *Catalog) SetEnabled(name string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.skills[name]; !ok {
		return fmt.Errorf("技能 %q 不存在", name)
	}

	was := c.disabled[name]
	if on {
		delete(c.disabled, name)
	} else {
		c.disabled[name] = true
	}

	if err := c.persistLocked(); err != nil {
		// 回滚
		if was {
			c.disabled[name] = true
		} else {
			delete(c.disabled, name)
		}
		return err
	}
	return nil
}

// IsEnabled 报告某个技能是否启用。技能不存在时返回 false。
func (c *Catalog) IsEnabled(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.skills[name]; !ok {
		return false
	}
	return !c.disabled[name]
}

// persistLocked 把当前禁用集写盘。调用方必须持有 c.mu。
func (c *Catalog) persistLocked() error {
	if c.statePath == "" {
		return nil
	}
	names := make([]string, 0, len(c.disabled))
	for name, off := range c.disabled {
		if off {
			names = append(names, name)
		}
	}
	sort.Strings(names) // 稳定输出，diff 才有意义
	return saveState(c.statePath, state{Disabled: names})
}

// Reference 读取技能的一项参考资料（L2 按需加载）。
func (s *Skill) Reference(rel string) (string, error) {
	// 防目录穿越：只接受 references/ 下的相对路径
	if !strings.HasPrefix(rel, "references/") || strings.Contains(rel, "..") {
		return "", fmt.Errorf("非法参考资料路径: %s", rel)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, rel))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// AutoActivate 返回命中触发词的技能正文，用于确定性触发。
//
// 与「模型自主判断」互补：某些技能是「永远该遵守的规矩」，
// 靠模型每次记得去 load 不可靠，命中关键词就直接注入。
func (c *Catalog) AutoActivate(text string) []*Skill {
	var out []*Skill
	for _, s := range c.All() {
		if s.MatchesTrigger(text) {
			out = append(out, s)
		}
	}
	return out
}
