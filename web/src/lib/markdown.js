// Markdown 渲染。对话里的回答几乎都是 Markdown，纯文本渲染会把
// 标题、列表、代码块全部压成一坨。
//
// 安全边界：模型输出属于不可信内容，最终经 {@html} 注入 DOM，
// 不经过任何消毒层。所以这里主动转义原始 HTML（html renderer）、
// 把链接统一加上 rel="noopener noreferrer" 并只允许安全协议。
// 这层防御有测试锁定（markdown.test.js）。

import { marked } from 'marked';
import hljs from 'highlight.js/lib/core';
import javascript from 'highlight.js/lib/languages/javascript';
import typescript from 'highlight.js/lib/languages/typescript';
import go from 'highlight.js/lib/languages/go';
import python from 'highlight.js/lib/languages/python';
import json from 'highlight.js/lib/languages/json';
import bash from 'highlight.js/lib/languages/bash';
import sql from 'highlight.js/lib/languages/sql';
import xml from 'highlight.js/lib/languages/xml';
import css from 'highlight.js/lib/languages/css';

// 按需注册：core + 8 种语言约 100 KB（gzip 后 ~30 KB），
// 全量 highlight.js 是它的 5 倍以上，对话场景用不了那么多语言。
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('go', go);
hljs.registerLanguage('python', python);
hljs.registerLanguage('json', json);
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('css', css);

marked.setOptions({
  gfm: true,
  breaks: true, // 单换行也当换行：聊天场景下更符合直觉
});

/** 只允许 http/https/mailto 与站内相对链接，挡掉 javascript: 等。 */
function safeHref(raw) {
  const href = String(raw || '').trim();
  if (!href) return '';
  // 去掉控制字符后再判断，避免 `java\nscript:` 绕过
  const normalized = href.replace(/[\u0000-\u001F\u007F]/g, '').toLowerCase();
  if (
    normalized.startsWith('http://') ||
    normalized.startsWith('https://') ||
    normalized.startsWith('mailto:') ||
    normalized.startsWith('/') ||
    normalized.startsWith('#')
  ) {
    return href;
  }
  return '';
}

const renderer = {
  // 模型输出不可信。marked 对原始 HTML 默认放行（文件头早先的注释
  // 声称「默认关闭」是错的，补测试时才暴露），而 {@html} 注入不经过
  // 任何消毒，所以这里必须显式转义——绝不能让模型直接输出 <script>。
  html({ text }) {
    return escapeHtml(text);
  },
  link({ href, title, tokens }) {
    const safe = safeHref(href);
    const text = this.parser.parseInline(tokens);
    if (!safe) return text; // 不安全的链接退化成纯文本
    const t = title ? ` title="${escapeAttr(title)}"` : '';
    return `<a href="${escapeAttr(safe)}" target="_blank" rel="noopener noreferrer"${t}>${text}</a>`;
  },
  // 代码块：高亮 + 语言标签 + 复制按钮。
  //
  // 复制按钮不带事件绑定——它在 {@html} 里生成，Svelte 够不到；
  // Message.svelte 用事件委托读 data-copy-code。
  code({ text, lang }) {
    const language = (lang || '').trim().toLowerCase();
    let body = escapeHtml(text);
    let langLabel = language;
    if (language && hljs.getLanguage(language)) {
      try {
        body = hljs.highlight(text, { language }).value;
      } catch {
        body = escapeHtml(text); // 高亮失败退回转义文本，绝不让一条消息炸掉
      }
    } else if (!language) {
      langLabel = 'text';
    }

    const header = `<div class="code-head"><span class="code-lang">${escapeHtml(langLabel)}</span>` +
      `<button type="button" class="code-copy" data-copy-code aria-label="复制代码">复制</button></div>`;
    const cls = language ? ` class="language-${escapeAttr(language)}"` : '';
    return `<div class="code-wrap">${header}<pre class="code-block"><code${cls}>${body}</code></pre></div>`;
  },
};

marked.use({ renderer });

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function escapeAttr(s) {
  return escapeHtml(s).replace(/'/g, '&#39;');
}

/**
 * 把 Markdown 渲染成 HTML。
 *
 * 流式输出时文本是半截的，未闭合的 ``` 会让 marked 把后面全部吞进代码块。
 * 这里做一次**补齐**：统计未闭合的围栏，临时补一个收尾，渲染完不影响原文。
 *
 * citations 是同一条消息里的引用块；有值时把正文里的 [n] 变成可点击
 * 标记。只在 marked 输出之后做替换，避免破坏代码块里的 [n]。
 */
export function renderMarkdown(text, citations = []) {
  if (!text) return '';
  let src = String(text);

  const fences = (src.match(/^```/gm) || []).length;
  if (fences % 2 === 1) {
    src += '\n```';
  }

  let html;
  try {
    html = marked.parse(src);
  } catch {
    // 渲染失败就退回纯文本，绝不让一条消息炸掉整个视图
    return `<p>${escapeHtml(text)}</p>`;
  }

  if (!citations?.length) return html;
  return linkCitations(html, citations);
}

/**
 * 把 HTML 里的 [n] 换成引用标记。
 *
 * 只处理文本片段，跳过 <pre>/<code> 内部——代码里的数组下标 [1]
 * 不能被当成引用。n 必须在 citations 里真实存在。
 */
function linkCitations(html, citations) {
  const valid = new Set(citations.map((c) => Number(c.index)).filter((n) => n > 0));
  if (!valid.size) return html;

  let inCode = 0;
  const parts = html.split(/(<[^>]+>)/g);
  return parts
    .map((part) => {
      if (part.startsWith('<')) {
        const tag = /^<\/?(pre|code)\b/i.exec(part);
        if (tag) {
          if (part[1] === '/') {
            inCode = Math.max(0, inCode - 1);
          } else {
            inCode++;
          }
        }
        return part;
      }
      if (inCode > 0 || !part.includes('[')) return part;
      return part.replace(/\[(\d+)\]/g, (whole, n) => {
        if (!valid.has(Number(n))) return whole;
        return `<button type="button" class="cite-mark" data-cite="${n}">${n}</button>`;
      });
    })
    .join('');
}
