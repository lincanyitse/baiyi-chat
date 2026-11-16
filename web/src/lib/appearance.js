// 外观偏好：主题与字号。
//
// 存在 localStorage，属于「这台机器上这个人的习惯」，不需要跨设备。
// 读写都包了 try/catch：隐私模式下 localStorage 会直接抛异常，
// 那时退回默认值，页面照常工作。

const THEME_KEY = 'baiyi.theme';
const FONT_KEY = 'baiyi.font';
const ACCENT_KEY = 'baiyi.accent';

const darkQuery = () => window.matchMedia('(prefers-color-scheme: dark)');

/** 强调色预设。key 写进 <html data-accent>，CSS 里覆写 --accent*。 */
export const accents = [
  { key: 'indigo', label: '靛蓝', swatch: '#4a5bd9' },
  { key: 'teal', label: '深青', swatch: '#2f6f6b' },
  { key: 'plum', label: '绛紫', swatch: '#8455a8' },
  { key: 'amber', label: '琥珀', swatch: '#a5671f' },
];

/** 主题：system | light | dark */
export function readTheme() {
  try {
    return localStorage.getItem(THEME_KEY) || 'system';
  } catch {
    return 'system';
  }
}

/** 字号：small | normal | large */
export function readFont() {
  try {
    return localStorage.getItem(FONT_KEY) || 'normal';
  } catch {
    return 'normal';
  }
}

/** 强调色：accents 里的 key，未知值退回 indigo */
export function readAccent() {
  try {
    const v = localStorage.getItem(ACCENT_KEY) || 'indigo';
    return accents.some((a) => a.key === v) ? v : 'indigo';
  } catch {
    return 'indigo';
  }
}

/**
 * 把偏好写到 <html> 上。
 *
 * 「跟随系统」在这里被解析成实际的 light/dark，而不是留空交给 CSS
 * 媒体查询——这样 CSS 只需要一套 :root[data-theme='dark']，也避免
 * 手动选亮色时被系统偏好盖回去。
 */
export function applyTheme(theme) {
  const root = document.documentElement;
  const resolved = theme === 'system' ? (darkQuery().matches ? 'dark' : 'light') : theme;
  root.setAttribute('data-theme', resolved);
}

export function applyFont(size) {
  const root = document.documentElement;
  if (size === 'normal') {
    root.removeAttribute('data-font');
  } else {
    root.setAttribute('data-font', size);
  }
}

/** 设置强调色，只改 <html data-accent>，具体色值由 CSS 定义。 */
export function applyAccent(key) {
  const root = document.documentElement;
  if (key === 'indigo') {
    root.removeAttribute('data-accent');
  } else {
    root.setAttribute('data-accent', key);
  }
}

/** 设置并持久化强调色。 */
export function setAccent(key) {
  if (!accents.some((a) => a.key === key)) return;
  applyAccent(key);
  try {
    localStorage.setItem(ACCENT_KEY, key);
  } catch {
    // 存不下就算了，本次会话内仍然生效
  }
}

/** 设置并持久化主题。 */
export function setTheme(theme) {
  applyTheme(theme);
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    // 存不下就算了，本次会话内仍然生效
  }
}

/** 设置并持久化字号。 */
export function setFont(size) {
  applyFont(size);
  try {
    localStorage.setItem(FONT_KEY, size);
  } catch {
    // 同上
  }
}

/** 系统偏好变化时，只有「跟随系统」模式才跟着变。 */
export function watchSystemTheme() {
  const q = darkQuery();
  const onChange = () => {
    if (readTheme() === 'system') applyTheme('system');
  };
  if (q.addEventListener) {
    q.addEventListener('change', onChange);
  } else {
    q.addListener(onChange);
  }
}

/** 启动时恢复偏好。main.js 在挂载前调用，避免闪一下默认主题。 */
export function restoreAppearance() {
  applyTheme(readTheme());
  applyFont(readFont());
  applyAccent(readAccent());
  watchSystemTheme();
}
