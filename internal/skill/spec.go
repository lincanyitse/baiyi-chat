// Package skill 实现技能的加载与渐进披露。
//
// 技能是数据不是代码：一个目录 + SKILL.md 就是一项技能，
// 放进 skills/ 即可生效，不需要重新编译。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec 是 SKILL.md 的 frontmatter。
type Spec struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Version      string   `yaml:"version"`
	Spec         int      `yaml:"spec"`
	Triggers     []string `yaml:"triggers"`
	AllowedTools []string `yaml:"allowed-tools"`
	Requires     []string `yaml:"requires"`
}

// Skill 是一项已加载的技能。
type Skill struct {
	Spec Spec
	// Dir 是技能目录的绝对路径。
	Dir string
	// Body 是 SKILL.md 的正文（去掉 frontmatter）。
	Body string
	// ContentHash 用于检测变更（Windows 上 mtime 精度不足，用内容哈希）。
	ContentHash string
	// References 是 references/ 下的文件相对路径。
	References []string
}

// Digest 是技能的 L0 目录条目：名字 + 一句话。
type Digest struct {
	Name        string
	Description string
}

// Load 从目录加载一项技能。
func Load(dir string) (*Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}

	spec, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if spec.Name == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 name", path)
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("%s: frontmatter 缺少 description", path)
	}
	if spec.Spec == 0 {
		spec.Spec = 1 // 缺省按 v1 处理
	}

	refs, err := listReferences(dir)
	if err != nil {
		return nil, err
	}

	return &Skill{
		Spec:        spec,
		Dir:         dir,
		Body:        body,
		ContentHash: hashString(string(raw)),
		References:  refs,
	}, nil
}

// splitFrontmatter 把 SKILL.md 拆成 frontmatter 与正文。
//
// 格式：以 --- 开头，下一个 --- 结束。没有 frontmatter 时返回错误——
// 技能必须有名字与描述，否则无法进入目录。
func splitFrontmatter(content string) (Spec, string, error) {
	var spec Spec

	if !strings.HasPrefix(content, "---") {
		return spec, "", fmt.Errorf("缺少 YAML frontmatter（文件应以 --- 开头）")
	}

	rest := content[3:]
	if i := strings.Index(rest, "\n"); i >= 0 {
		rest = rest[i+1:]
	} else {
		return spec, "", fmt.Errorf("frontmatter 未闭合")
	}

	end := strings.Index(rest, "\n---")
	if end < 0 {
		return spec, "", fmt.Errorf("frontmatter 未闭合（缺少结束的 ---）")
	}

	header := rest[:end]
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")

	if err := yaml.Unmarshal([]byte(header), &spec); err != nil {
		return spec, "", fmt.Errorf("解析 frontmatter: %w", err)
	}
	return spec, body, nil
}

// listReferences 列出 references/ 下的 Markdown 文件。
func listReferences(dir string) ([]string, error) {
	refDir := filepath.Join(dir, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, filepath.Join("references", e.Name()))
	}
	return out, nil
}

// MatchesTrigger 报告用户输入是否命中确定性触发词。
func (s *Skill) MatchesTrigger(text string) bool {
	for _, t := range s.Spec.Triggers {
		if t != "" && strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// Digest 返回 L0 目录条目。
func (s *Skill) Digest() Digest {
	return Digest{Name: s.Spec.Name, Description: s.Spec.Description}
}
