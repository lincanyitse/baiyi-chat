// 把 API 错误翻成用户能照做的话。
//
// 界面不该直接显示 err.message：后端有些错误已经是中文（比如
// 「任务 chat 没有可用的 Provider」），但网络层与 HTTP 状态类的
// 仍然是技术原文。这里按 ApiError 的 status 与 code 兜一层。

import { ApiError } from './api.js';

/**
 * 返回一句能展示给用户的话。
 *
 * 后端已经翻译过的错误（中文）原样透传——不要在两层各翻一次，
 * 不然同一件事会有两种说法。
 */
export function humanize(err) {
  if (!err) return '';

  const raw = err.message || String(err);

  // 已经是中文：后端翻过了，直接用
  if (/[一-龥]/.test(raw)) return raw;

  if (err instanceof ApiError) {
    switch (err.code) {
      case 'network':
        return '连不上白泽服务。确认它还在运行（终端窗口没关）。';
      case 'unauthorized':
        return '登录已失效，请重新登录。';
      case 'not_found':
        return '请求的内容不存在，可能已被删除。';
      case 'conflict':
        return '操作冲突，刷新后再试。';
      case 'unavailable':
        return '服务暂时不可用，稍后再试。';
      case 'bad_response':
        return '服务返回了无法识别的内容。详见日志。';
      case 'internal':
        return '服务内部出错。详见日志。';
      case 'bad_request':
        return '请求参数有问题：' + raw;
    }
    if (err.status >= 500) return '服务内部出错。详见日志。';
  }

  return raw;
}
