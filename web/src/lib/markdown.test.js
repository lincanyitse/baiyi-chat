import { describe, it, expect } from 'vitest';
import { renderMarkdown } from './markdown.js';

// 引用是安全边界的一部分：[n] 必须真实存在于 citations 才变按钮，
// 代码块里的数组下标不能被误当成引用。
describe('renderMarkdown 引用', () => {
  const cites = [
    { index: 1, doc_id: 'd1' },
    { index: 2, doc_id: 'd2' },
  ];

  it('正文里的 [1] 变成可点击标记', () => {
    const html = renderMarkdown('结论 [1]。', cites);
    expect(html).toContain('data-cite="1"');
    expect(html).toContain('cite-mark');
  });

  it('不在 citations 里的编号保持原样', () => {
    const html = renderMarkdown('见 [3] 与 [9]。', cites);
    expect(html).not.toContain('data-cite');
    expect(html).toContain('[3]');
    expect(html).toContain('[9]');
  });

  it('代码块里的 [1] 不被替换', () => {
    const src = '正文 [1]。\n\n```go\narr[1] = x\n```';
    const html = renderMarkdown(src, cites);
    // 正文部分有按钮
    expect(html).toContain('data-cite="1"');
    // 代码块部分保持原样：hljs 高亮会把数字包进 span，
    // 所以断言「代码块内没有引用标记」而不是「字面 arr[1]」
    const codeBlock = html.match(/<div class="code-wrap">[\s\S]*?<\/div>\s*$/)?.[0] || '';
    expect(codeBlock).toContain('arr[');
    expect(codeBlock).not.toContain('data-cite');
  });

  it('行内代码里的 [1] 也不被替换', () => {
    const html = renderMarkdown('用 `arr[1]` 取值，见 [1]。', cites);
    const code = html.match(/<code>[^<]*<\/code>/)?.[0] || '';
    expect(code).toContain('arr[1]');
    expect(code).not.toContain('cite-mark');
    expect(html).toContain('data-cite="1"');
  });

  it('没有 citations 时原样渲染', () => {
    const html = renderMarkdown('数组 arr[1] 取值');
    expect(html).not.toContain('cite-mark');
    expect(html).toContain('arr[1]');
  });

  it('同一编号出现多次都替换', () => {
    const html = renderMarkdown('[1] 然后 [1] 再 [2]。', cites);
    expect(html.match(/data-cite="1"/g)?.length).toBe(2);
    expect(html.match(/data-cite="2"/g)?.length).toBe(1);
  });
});

describe('renderMarkdown 代码块', () => {
  it('有语言时输出语言标签与复制按钮', () => {
    const html = renderMarkdown('```go\nfmt.Println()\n```');
    expect(html).toContain('code-head');
    expect(html).toContain('>go<');
    expect(html).toContain('data-copy-code');
  });

  it('未知语言也显示标签', () => {
    const html = renderMarkdown('```wasm\nx\n```');
    expect(html).toContain('>wasm<');
  });

  it('无语言时显示 text', () => {
    const html = renderMarkdown('```\nplain\n```');
    expect(html).toContain('>text<');
  });

  it('注册过的语言产出 hljs 类', () => {
    const html = renderMarkdown('```go\nfunc main() {}\n```');
    expect(html).toContain('hljs-keyword');
  });

  it('未注册语言不高亮但不报错', () => {
    const html = renderMarkdown('```rustlang\nfn main() {}\n```');
    expect(html).toContain('code-wrap');
    expect(html).not.toContain('hljs-keyword');
  });
});

describe('renderMarkdown 链接安全', () => {
  it('javascript: 链接退化为纯文本', () => {
    const html = renderMarkdown('[点我](javascript:alert(1))');
    expect(html).not.toContain('<a href');
    expect(html).toContain('点我');
  });

  it('换行注入的 javascript: 也被挡住', () => {
    const html = renderMarkdown('[x](java\nscript:alert(1))');
    expect(html).not.toContain('javascript:');
  });

  it('https 链接带 rel="noopener noreferrer"', () => {
    const html = renderMarkdown('[官网](https://example.com)');
    expect(html).toContain('rel="noopener noreferrer"');
    expect(html).toContain('target="_blank"');
  });
});

describe('renderMarkdown 围栏补齐', () => {
  it('流式未闭合的 ``` 被补齐，不吞掉后面的内容', () => {
    const html = renderMarkdown('```go\nx := 1');
    expect(html).toContain('code-wrap');
    // hljs 会把数字包进 span，断言能匹配高亮后的结构
    expect(html).toContain('x :=');
    expect(html).toContain('hljs-number');
  });
});

describe('renderMarkdown 转义', () => {
  it('HTML 注入被转义', () => {
    const html = renderMarkdown('<script>alert(1)</script>');
    expect(html).not.toContain('<script>');
    expect(html).toContain('&lt;script&gt;');
  });

  it('空输入返回空串', () => {
    expect(renderMarkdown('')).toBe('');
    expect(renderMarkdown(null)).toBe('');
  });
});
