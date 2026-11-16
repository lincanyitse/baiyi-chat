package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lincanyitse/baiyi-chat/internal/tool"
)

// FileWrite 在工作目录内写入文本文件。
//
// 风险等级是 RiskWrite：它会改变磁盘状态，但不是不可逆的破坏性
// 操作（不删、不执行、不出网），因此走「牢笼内自动 / 越界需确认」
// 的审批策略。
//
// 路径牢笼是硬约束：root 之外一律拒绝，不做相对路径逃逸。
// 这不是审批能放行的事——审批解决的是「用户是否知情」，
// 而越界写入是设计上就不该发生的事。
type FileWrite struct {
	root string
}

// NewFileWrite 构造工具。root 是允许写入的根目录。
func NewFileWrite(root string) *FileWrite {
	return &FileWrite{root: root}
}

// Spec 实现 tool.Tool。
func (t *FileWrite) Spec() tool.Spec {
	return tool.DefaultSpec(tool.Spec{
		Name: "file_write",
		Description: "把文本内容写入工作目录内的文件。用于保存笔记、生成报告、输出代码。" +
			"只能写入工作目录及其子目录，不能写到系统其他位置。" +
			"已存在的文件会被覆盖——需要保留原内容时先读取再决定。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "相对于工作目录的文件路径，如 notes/todo.md"
				},
				"content": {
					"type": "string",
					"description": "要写入的完整文本内容"
				},
				"append": {
					"type": "boolean",
					"description": "为真时追加到文件末尾，默认为覆盖"
				}
			},
			"required": ["path", "content"],
			"additionalProperties": false
		}`),
		Risk:    tool.RiskWrite,
		Timeout: 10 * time.Second,
	})
}

// Execute 实现 tool.Tool。
func (t *FileWrite) Execute(ctx context.Context, in json.RawMessage, ec *tool.ExecContext) (tool.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return tool.ErrorResult("参数解析失败: " + err.Error()), nil
	}
	if strings.TrimSpace(args.Path) == "" {
		return tool.ErrorResult("path 不能为空"), nil
	}

	abs, err := t.resolve(args.Path)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if args.Append {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}

	if dir := filepath.Dir(abs); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return tool.ErrorResult("创建目录失败: " + err.Error()), nil
		}
	}

	f, err := os.OpenFile(abs, flag, 0o644)
	if err != nil {
		return tool.ErrorResult("打开文件失败: " + err.Error()), nil
	}
	defer f.Close()

	n, err := f.WriteString(args.Content)
	if err != nil {
		return tool.ErrorResult("写入失败: " + err.Error()), nil
	}

	verb := "写入"
	if args.Append {
		verb = "追加"
	}
	return tool.TextResult(fmt.Sprintf("已%s %s（%d 字节）", verb, args.Path, n)), nil
}

// resolve 把用户给的相对路径解析成绝对路径，并确认它落在牢笼内。
//
// 用 filepath.Clean 之后再比较前缀：`../../etc/passwd` 这类输入
// 在 Clean 之后会变成牢笼外的路径，直接被拒。为了防住符号链接逃逸，
// 对已存在的路径取 EvalSymlinks 再校验一次。
func (t *FileWrite) resolve(p string) (string, error) {
	root, err := filepath.Abs(t.root)
	if err != nil {
		return "", fmt.Errorf("工作目录无效: %w", err)
	}

	// 绝对路径也必须落在牢笼内，不做特殊放行
	candidate := p
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)

	if !within(root, candidate) {
		return "", fmt.Errorf("路径 %q 超出工作目录 %s，拒绝写入", p, root)
	}

	// 已存在的路径要防符号链接逃逸；不存在的文件其父目录仍然要检查
	if real, err := filepath.EvalSymlinks(candidate); err == nil {
		if !within(root, real) {
			return "", fmt.Errorf("路径 %q 经符号链接后超出工作目录，拒绝写入", p)
		}
	} else if realDir, err2 := filepath.EvalSymlinks(filepath.Dir(candidate)); err2 == nil {
		if !within(root, filepath.Join(realDir, filepath.Base(candidate))) {
			return "", fmt.Errorf("路径 %q 经符号链接后超出工作目录，拒绝写入", p)
		}
	}

	return candidate, nil
}

// within 报告 path 是否等于 root 或位于其下。
//
// 用「root + 分隔符」做前缀，避免 /data/work-other 被误判为
// /data/work 的子路径。
func within(root, path string) bool {
	if path == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}

var _ tool.Tool = (*FileWrite)(nil)
