package config

// MCP 配置写回。
//
// 与 Provider 写回共用同一套「外科手术式替换」：只重写 [[mcp]] 段，
// 其余字节（注释、手写排版、其他段）原样保留。整份重新序列化会丢掉
// 用户写的注释，风险太大。

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SaveMCP 把 MCP server 列表写回配置文件。
//
// 返回写回后的完整配置，供调用方刷新展示快照。
func SaveMCP(path string, servers []MCPServer) (Config, error) {
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("读取配置失败: %w", err)
	}

	section, err := renderMCP(servers)
	if err != nil {
		return Config{}, err
	}

	out := replaceSections(string(orig), section, isMCPHeader)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return Config{}, fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Config{}, fmt.Errorf("替换配置文件失败: %w", err)
	}

	return Load(path)
}

// renderMCP 把 server 列表渲染成 [[mcp]] 段。
//
// 按 alias 排序：同样的配置每次写出同样的文本，diff 才有意义。
//
// 不写注释头：replaceSections 只认段头，注释会随着每次保存不断累积。
// 用户自己的注释也一样——所以这里只产出纯数据段，保持写回幂等。
func renderMCP(servers []MCPServer) (string, error) {
	sorted := append([]MCPServer(nil), servers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Alias < sorted[j].Alias })

	var b strings.Builder

	for i, s := range sorted {
		if s.Alias == "" {
			return "", fmt.Errorf("MCP server 缺少 alias")
		}
		// 只在非首个之前空一行：首个前面加了会让每次保存都累积一个空行
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[[mcp]]\n")
		fmt.Fprintf(&b, "alias     = %s\n", quote(s.Alias))
		fmt.Fprintf(&b, "transport = %s\n", quote(s.Transport))
		if s.Command != "" {
			fmt.Fprintf(&b, "command   = %s\n", quote(s.Command))
		}
		if len(s.Args) > 0 {
			quoted := make([]string, len(s.Args))
			for i, a := range s.Args {
				quoted[i] = quote(a)
			}
			fmt.Fprintf(&b, "args      = [%s]\n", strings.Join(quoted, ", "))
		}
		if s.URL != "" {
			fmt.Fprintf(&b, "url       = %s\n", quote(s.URL))
		}
		if len(s.AllowedTools) > 0 {
			quoted := make([]string, len(s.AllowedTools))
			for i, t := range s.AllowedTools {
				quoted[i] = quote(t)
			}
			fmt.Fprintf(&b, "allowed_tools = [%s]\n", strings.Join(quoted, ", "))
		}
		if s.Risk != "" && s.Risk != "read" {
			fmt.Fprintf(&b, "risk      = %s\n", quote(s.Risk))
		}
		fmt.Fprintf(&b, "enabled   = %t\n", s.Enabled)
	}

	// 交给 TOML 解析器自检：手写文本有语法错要在落盘前发现
	var probe map[string]any
	if err := toml.Unmarshal([]byte(b.String()), &probe); err != nil {
		return "", fmt.Errorf("生成的 MCP 配置有语法错误: %w", err)
	}
	return b.String(), nil
}
