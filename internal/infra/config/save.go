package config

// 配置写回。
//
// 只允许改 Provider 与 Route 两段——这是管理界面唯一需要写的东西。
// 整份文件重新序列化的风险太高：注释会丢、手写的排版会被打乱，
// 而且万一 Marshal 漏了某个字段，用户的其他配置就被静默抹掉了。
//
// 做法是「外科手术式替换」：把原文件按段落切开，只重写 [providers.*]
// 与 [route.*] 的文本块，其余字节原样保留。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SaveProviders 把 Provider 与 Route 段写回配置文件。
//
// 返回写回后的完整配置：调用方拿它重建 Registry，避免再读一次盘。
func SaveProviders(path string, providers map[string]Provider, routes map[string]Route) (Config, error) {
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("读取配置失败: %w", err)
	}

	section, err := renderProviders(providers, routes)
	if err != nil {
		return Config{}, err
	}

	out := replaceSections(string(orig), section, isProviderHeader)

	// 先写临时文件再改名：写完一半崩溃不会留下半截配置
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Config{}, err
	}
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return Config{}, fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Config{}, fmt.Errorf("替换配置文件失败: %w", err)
	}

	// 立刻重新加载，让调用方拿到生效后的配置；解析失败说明写坏了，
	// 但文件已经落盘——所以这里把错误原样抛出，由调用方决定是否回滚。
	return Load(path)
}

// renderProviders 把 Provider 与 Route 渲染成 TOML 文本。
func renderProviders(providers map[string]Provider, routes map[string]Route) (string, error) {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	// 排序保证同样的配置每次写出同样的文本，diff 才有意义
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		p := providers[name]
		// 手写而不是 toml.Marshal：需要保留字段顺序与只输出非空字段，
		// 而且 Provider 上的 struct tag 是 koanf 的不是 toml 的。
		fmt.Fprintf(&b, "[providers.%s]\n", quoteKey(name))
		fmt.Fprintf(&b, "kind        = %s\n", quote(p.Kind))
		if p.BaseURL != "" {
			fmt.Fprintf(&b, "base_url    = %s\n", quote(p.BaseURL))
		}
		if p.APIKeyEnv != "" {
			fmt.Fprintf(&b, "api_key_env = %s\n", quote(p.APIKeyEnv))
		}
		if p.TimeoutS > 0 {
			fmt.Fprintf(&b, "timeout_s   = %d\n", p.TimeoutS)
		}
		for k, v := range p.Headers {
			fmt.Fprintf(&b, "\n[providers.%s.headers]\n%s = %s\n", quoteKey(name), quoteKey(k), quote(v))
		}
		b.WriteString("\n")

		modelIDs := make([]string, 0, len(p.Models))
		for id := range p.Models {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		for _, id := range modelIDs {
			m := p.Models[id]
			fmt.Fprintf(&b, "[providers.%s.models.%s]\n", quoteKey(name), quoteKey(id))
			writeModel(&b, m)
			b.WriteString("\n")
		}
	}

	taskNames := make([]string, 0, len(routes))
	for task := range routes {
		taskNames = append(taskNames, task)
	}
	sort.Strings(taskNames)
	for _, task := range taskNames {
		cands := routes[task].Candidates
		quoted := make([]string, len(cands))
		for i, c := range cands {
			quoted[i] = quote(c)
		}
		fmt.Fprintf(&b, "[route.%s]\ncandidates = [%s]\n\n", quoteKey(task), strings.Join(quoted, ", "))
	}

	// 交给 TOML 解析器自检一次：手写的文本有语法错要在落盘前发现
	var probe map[string]any
	if err := toml.Unmarshal([]byte(b.String()), &probe); err != nil {
		return "", fmt.Errorf("生成的配置有语法错误: %w", err)
	}
	return b.String(), nil
}

func writeModel(b *strings.Builder, m Model) {
	fmt.Fprintf(b, "context_window = %d\n", m.ContextWindow)
	if m.MaxOutput > 0 {
		fmt.Fprintf(b, "max_output     = %d\n", m.MaxOutput)
	}
	fmt.Fprintf(b, "tools          = %t\n", m.Tools)
	if m.Vision {
		fmt.Fprintf(b, "vision         = %t\n", m.Vision)
	}
	if m.Thinking {
		fmt.Fprintf(b, "thinking       = %t\n", m.Thinking)
	}
	if m.PrefixCache {
		fmt.Fprintf(b, "prefix_cache   = %t\n", m.PrefixCache)
	}
	if m.StrictSchema {
		fmt.Fprintf(b, "strict_schema  = %t\n", m.StrictSchema)
	}
	if m.PriceIn != 0 {
		fmt.Fprintf(b, "price_in       = %v\n", m.PriceIn)
	}
	if m.PriceOut != 0 {
		fmt.Fprintf(b, "price_out      = %v\n", m.PriceOut)
	}
	if m.PriceCached != 0 {
		fmt.Fprintf(b, "price_cached   = %v\n", m.PriceCached)
	}
}

// replaceSections 删掉原文里 owned 判定为真的段，再把新段插到
// 第一个被删的位置（没有则追加到末尾）。
//
// 按行扫描而不是正则：TOML 的段头可能带引号、可能在数组元素里出现
// 类似文本，用文本模式匹配更安全。
//
// owned 由调用方给出而不是写死：Provider 写回只该动 providers/route，
// MCP 写回只该动 [[mcp]]。早先这里写死了「全部由本包管理的段」，
// 结果保存 MCP 时把用户配好的 Provider 段一起删了——静默数据丢失。
func replaceSections(original, section string, owned func(line string) bool) string {
	// 空文件经 Split 会得到 [""]，那个空串会被当成「保留的一行」，
	// 导致新段前面多出一个空行。先裁掉首尾空行再处理。
	lines := strings.Split(strings.Trim(original, "\n"), "\n")

	var kept []string
	insertAt := -1

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if owned(trimmed) {
			if insertAt < 0 {
				insertAt = len(kept)
			}
			// 跳过整个段：直到下一个段头或文件末尾
			for i+1 < len(lines) && !isSectionHeader(strings.TrimSpace(lines[i+1])) {
				i++
			}
			continue
		}
		kept = append(kept, line)
	}

	body := strings.Join(kept, "\n")
	// 把新段接在被删位置；原文件没有这些段时接在末尾
	if insertAt < 0 {
		body = joinSection(strings.TrimRight(body, "\n"), section)
	} else {
		// insertAt 是 kept 中的下标，需要换算回文本位置
		before := strings.TrimRight(strings.Join(kept[:insertAt], "\n"), "\n")
		after := strings.TrimLeft(strings.Join(kept[insertAt:], "\n"), "\n")
		body = joinSection(before, section) + after
	}
	return strings.TrimRight(body, "\n") + "\n"
}

// joinSection 把新段接在 before 之后，中间空一行。
//
// before 为空时不能加前导空行：否则每次保存都会在文件头累积一个空行，
// 三次之后配置就变成「一片空白开头」。
func joinSection(before, section string) string {
	if before == "" {
		return section
	}
	return before + "\n\n" + section
}

// isSectionHeader 判断一行是否是 TOML 段头。
func isSectionHeader(line string) bool {
	return strings.HasPrefix(line, "[")
}

// isProviderHeader 判断是否是 Provider 写回负责的段。
func isProviderHeader(line string) bool {
	return strings.HasPrefix(line, "[providers.") ||
		strings.HasPrefix(line, "[providers]") ||
		strings.HasPrefix(line, "[route.") ||
		strings.HasPrefix(line, "[route]")
}

// isMCPHeader 判断是否是 MCP 写回负责的段。
func isMCPHeader(line string) bool {
	return line == "[[mcp]]"
}

// quote 把字符串渲染成 TOML 基本字符串。
//
// TOML 基本字符串的转义规则与 Go 的 %q 基本一致（\" \\ \n \t \uXXXX），
// strconv.Quote 已经覆盖，不必每次再走一遍 toml.Marshal 的反射。
func quote(s string) string {
	return strconv.Quote(s)
}

// quoteKey 把键名渲染成 TOML 键；需要时加引号。
func quoteKey(k string) string {
	for _, r := range k {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return quote(k)
		}
	}
	return k
}
