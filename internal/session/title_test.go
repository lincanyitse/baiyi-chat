package session

import "testing"

// cleanTitle 是命名链路里唯一可单独测的部分：模型的输出不可控，
// 清洗必须把各种噪声都吃掉，否则会原样出现在会话列表里。
func TestCleanTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"纯标题", "修复 SSE 断线续传", "修复 SSE 断线续传"},
		{"去引号", `"修复 SSE 断线续传"`, "修复 SSE 断线续传"},
		{"去中文引号", "「修复 SSE 断线续传」", "修复 SSE 断线续传"},
		{"去反引号", "`修复 SSE`", "修复 SSE"},
		{"去句号", "修复 SSE 断线续传。", "修复 SSE 断线续传"},
		{"去 Markdown 强调", "**修复 SSE**", "修复 SSE"},
		{"只取第一行", "修复 SSE\n解释：因为……", "修复 SSE"},
		{"去首尾空白", "  修复 SSE  ", "修复 SSE"},
		{"空输入", "   ", ""},
		{"只有引号", `""`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanTitle(tc.in); got != tc.want {
				t.Errorf("cleanTitle(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// 超长标题要按字符截断，不能把中文截出半个字。
func TestCleanTitleTruncatesByRune(t *testing.T) {
	long := "这是一个非常非常非常非常非常非常非常非常长的标题需要被截断"
	got := cleanTitle(long)

	if n := len([]rune(got)); n > titleMaxRunes {
		t.Errorf("标题应截到 %d 字符以内，实际 %d", titleMaxRunes, n)
	}
	// 截断后仍应是合法的 UTF-8（按 rune 截断天然满足，
	// 但按字节截断就会出错——这条断言防的是回归）
	for _, r := range got {
		if r == '�' {
			t.Errorf("截断产生了非法字符: %q", got)
		}
	}
}

// 控制字符会破坏列表渲染，必须清掉。
func TestCleanTitleStripsControlChars(t *testing.T) {
	got := cleanTitle("标题\x00带\x01控制字符")
	if got != "标题带控制字符" {
		t.Errorf("控制字符未被清除: %q", got)
	}
}

// 送进模型的原文要限长，但短文本不能被改动。
func TestTruncateForTitle(t *testing.T) {
	short := "短问题"
	if got := truncateForTitle(short); got != short {
		t.Errorf("短文本不该被改动: %q", got)
	}

	long := make([]rune, 800)
	for i := range long {
		long[i] = '字'
	}
	got := truncateForTitle(string(long))
	if n := len([]rune(got)); n != 500 {
		t.Errorf("长文本应截到 500 字符，实际 %d", n)
	}
}
