// 管理面板的数据访问。与会话 API 分开文件：这些接口是只读的
// 诊断视图，不属于核心对话路径。

import { request } from './api.js';

/** 管理面板用到的只读接口。 */
export const adminApi = {
  providers: () => request('GET', '/api/providers'),
  cost: (range = 'today') => request('GET', `/api/cost?range=${encodeURIComponent(range)}`),
  /** 单个会话的成本。会话内可能有多轮、每轮多次调用，按全部轮次汇总。 */
  sessionCost: (sessionId) =>
    request('GET', `/api/cost?session=${encodeURIComponent(sessionId)}`),
};

/** 把 token 数转成易读的短形式。 */
export function shortTokens(n) {
  if (!n) return '0';
  if (n < 1000) return String(n);
  if (n < 1_000_000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
  return (n / 1_000_000).toFixed(2).replace(/\.?0+$/, '') + 'M';
}

/** 费用格式化。未定价时显示「—」而不是 0，避免误导。 */
export function formatCost(n, priced = true) {
  if (!priced) return '未定价';
  if (!n) return '¥0';
  if (n < 0.01) return '¥' + n.toFixed(4);
  return '¥' + n.toFixed(2);
}
