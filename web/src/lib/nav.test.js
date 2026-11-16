import { describe, it, expect } from 'vitest';
import { navGroups, navItems, itemsOfGroup } from './nav.js';

// 分组导航是首屏、能力一览、设置页三处共用的数据源，
// 结构错了三处一起错，所以锁定不变量。

describe('nav 数据', () => {
  it('每个分组至少有一项', () => {
    for (const g of navGroups) {
      expect(itemsOfGroup(g.key).length, `分组 ${g.key} 不该为空`).toBeGreaterThan(0);
    }
  });

  it('每个条目都属于已声明的分组', () => {
    const keys = new Set(navGroups.map((g) => g.key));
    for (const it of navItems) {
      expect(keys.has(it.group), `${it.key} 的 group ${it.group} 未声明`).toBe(true);
    }
  });

  it('条目 key 唯一（AdminPanel 靠它切 tab）', () => {
    const keys = navItems.map((it) => it.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('每个条目的字段齐全（导航与能力一览都渲染它们）', () => {
    for (const it of navItems) {
      expect(it.label, `${it.key} 缺 label`).toBeTruthy();
      expect(it.desc, `${it.key} 缺 desc`).toBeTruthy();
      expect(it.icon, `${it.key} 缺 icon`).toBeTruthy();
    }
  });

  it('分组无重复', () => {
    const keys = navGroups.map((g) => g.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('itemsOfGroup 不含其它分组的项', () => {
    const all = itemsOfGroup(navGroups[0].key);
    for (const it of all) {
      expect(it.group).toBe(navGroups[0].key);
    }
  });
});
