import { describe, it, expect, vi, beforeEach } from 'vitest';

// status.js 依赖 api.js 与 admin.js 的网络层，这里整体 mock 掉，
// 只测缓存行为与计数逻辑。

const calls = { health: 0, skills: 0, mcp: 0, models: 0, memory: 0, cost: 0 };

vi.mock('./api.js', () => ({
  api: {
    health: vi.fn(async () => { calls.health++; return { status: 'ok' }; }),
    skills: vi.fn(async () => { calls.skills++; return { skills: [{ enabled: true }, { enabled: false }] }; }),
    mcp: vi.fn(async () => {
      calls.mcp++;
      return { servers: [
        { connected: true, tools: [{}, {}, {}] },
        { connected: false, tools: [{}] },
      ] };
    }),
    configuredModels: vi.fn(async () => { calls.models++; return { models: [{}, {}] }; }),
    memoryStats: vi.fn(async () => { calls.memory++; return { active_facts: 7 }; }),
  },
}));

vi.mock('./admin.js', () => ({
  adminApi: {
    cost: vi.fn(async () => { calls.cost++; return { calls: 4 }; }),
  },
}));

// 缓存是模块私有状态，每个用例重新 import 拿到干净的实例
async function fresh() {
  vi.resetModules();
  return import('./status.js');
}

beforeEach(() => {
  vi.clearAllMocks();
  for (const k of Object.keys(calls)) calls[k] = 0;
});

describe('loadOverview', () => {
  it('计数来自多个接口的汇总', async () => {
    const { loadOverview } = await fresh();
    const s = await loadOverview();
    expect(s).toEqual({
      online: true,
      skills: 1, // 2 个技能里启用 1 个
      plugins: 1, // 2 个 server 里连接 1 个
      tools: 4, // 3 + 1
      models: 2,
      facts: 7,
      calls: 4,
    });
  });

  it('30 秒内重复调用命中缓存，不发新请求', async () => {
    const { loadOverview } = await fresh();
    await loadOverview();
    const after = { ...calls };
    await loadOverview();
    expect(calls).toEqual(after);
  });

  it('单个接口挂掉退回默认值，不拖垮整屏', async () => {
    const { loadOverview } = await fresh();
    const { api } = await import('./api.js');
    api.skills.mockRejectedValueOnce(new Error('boom'));
    const s = await loadOverview();
    expect(s.skills).toBe(0); // fallback 的空数组
    expect(s.online).toBe(true); // 其余不受影响
  });
});
