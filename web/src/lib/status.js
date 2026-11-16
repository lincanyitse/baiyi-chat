// 首屏与能力一览共用的「当前装备状态」。
//
// 两个面板原本各自拉一遍 /api/skills、/api/mcp、/api/memory/stats，
// 而且都是串行 await（首屏多等 4 个 RTT）。收敛成一处后数字口径
// 不会分叉，请求也只发一批。
//
// 单个接口挂了不该让整屏状态消失，所以逐项 catch 填默认值。

import { api } from './api.js';
import { adminApi } from './admin.js';

const fallback = (fn, value) => fn().catch(() => value);

// 短 TTL 缓存：首屏与能力一览、以及在设置页里来回切 tab 都会用到
// 这批数字。30 秒内共享一次请求，又不会旧到误导。
const TTL = 30_000;
let cache = null; // { at, data }

/** 拉取一次完整状态，返回可直接渲染的计数字段。 */
export async function loadOverview() {
  if (cache && Date.now() - cache.at < TTL) return cache.data;

  const [health, skills, mcp, models, memory, cost] = await Promise.all([
    fallback(() => api.health(), null),
    fallback(() => api.skills(), { skills: [] }),
    fallback(() => api.mcp(), { servers: [] }),
    fallback(() => api.configuredModels(), { models: [] }),
    fallback(() => api.memoryStats(), {}),
    fallback(() => adminApi.cost('today'), {}),
  ]);

  const servers = mcp.servers || [];
  const data = {
    online: health?.status === 'ok',
    skills: (skills.skills || []).filter((s) => s.enabled).length,
    plugins: servers.filter((s) => s.connected).length,
    tools: servers.reduce((n, s) => n + (s.tools?.length || 0), 0),
    models: (models.models || []).length,
    facts: memory.active_facts || 0,
    calls: cost.calls || 0,
  };
  cache = { at: Date.now(), data };
  return data;
}
