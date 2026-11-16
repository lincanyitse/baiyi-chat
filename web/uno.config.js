import { defineConfig, presetWind3 } from 'unocss';

// UnoCSS 的地基规则：主题全部指向 CSS 变量。
//
// app.css 里的 --* 令牌是唯一真源（被组件引用 1200+ 次，不能改名）。
// 原子类只是这些令牌的快捷写法，所以 bg-accent / border-border 会
// 自动跟随明暗主题切换，不需要在 UnoCSS 里再维护一套暗色。
//
// 不用 presetIcons：那会引整个 iconify 数据包。图标继续用
// Icon.svelte 的内联 SVG，按需扩充。

export default defineConfig({
  presets: [presetWind3({ preflight: false })],

  theme: {
    colors: {
      bg: 'var(--bg)',
      'bg-subtle': 'var(--bg-subtle)',
      surface: 'var(--surface)',
      'surface-2': 'var(--surface-2)',
      'surface-hover': 'var(--surface-hover)',
      fg: 'var(--fg)',
      'fg-secondary': 'var(--fg-secondary)',
      muted: 'var(--muted)',
      faint: 'var(--faint)',
      border: 'var(--border)',
      'border-strong': 'var(--border-strong)',
      accent: 'var(--accent)',
      'accent-hover': 'var(--accent-hover)',
      'accent-soft': 'var(--accent-soft)',
      'accent-fg': 'var(--accent-fg)',
      brand: 'var(--brand)',
      'brand-soft': 'var(--brand-soft)',
      danger: 'var(--danger)',
      'danger-soft': 'var(--danger-soft)',
      success: 'var(--success)',
      warning: 'var(--warning)',
      transparent: 'transparent',
      current: 'currentColor',
    },
    borderRadius: {
      sm: 'var(--r-sm)',
      md: 'var(--r-md)',
      lg: 'var(--r-lg)',
      xl: 'var(--r-xl)',
      full: 'var(--r-full)',
    },
    boxShadow: {
      sm: 'var(--shadow-sm)',
      md: 'var(--shadow-md)',
      lg: 'var(--shadow-lg)',
    },
    fontFamily: {
      sans: 'var(--sans)',
      mono: 'var(--mono)',
      brand: 'var(--brand-font)',
    },
  },

  // shortcut 一律用 u- 前缀，避免和组件里残留的 scoped 类同名。
  // 预备的 u-card / u-btn / u-input 等没有被引用，而且用了会失效的
  // border、shadow-* 和 rem 间距，迁移结束后已删除。
  shortcuts: {
    // 字号跟 app.css 的变量一致，不走 Wind 默认刻度。
    'u-fs-xs': 'text-[length:var(--fs-xs)]',
    'u-fs-sm': 'text-[length:var(--fs-sm)]',
    'u-box': 'bg-surface rounded-md [border:1px_solid_var(--border)]',
    // preflight 关闭后，shadow-* 与 tabular-nums 会引用未初始化的 --un-* 变量，
    // 整条声明被浏览器丢弃。投影写 [box-shadow:var(--shadow-*)],
    // 等宽数字写 [font-variant-numeric:tabular-nums]。
    'u-field-focus':
      '[outline:none] focus:border-accent focus:[box-shadow:0_0_0_3px_var(--accent-soft)]',
    'u-btn-accent':
      '[border:none] rounded-md bg-accent text-accent-fg cursor-pointer u-fs-sm enabled:hover:bg-accent-hover disabled:opacity-50 disabled:cursor-default',
  },

  // 动态类必须显式列出，否则静态提取会漏掉。
  // 规则：代码里禁止 `class="text-${c}"` 这种拼接，一律在常量里写全类名。
  // 迁移开始时按需往这里加，避免预生成一批从未被引用的规则。
  safelist: [],
});
