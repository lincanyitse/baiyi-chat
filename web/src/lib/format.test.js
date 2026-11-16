import { describe, it, expect } from 'vitest';
import { formatDateTime, formatTime, formatSize } from './format.js';

// 这些函数是多个面板共用的展示口径，补零与否、单位换算错了会
// 各处一起错。

describe('formatSize', () => {
  it('0 与负值显示占位符', () => {
    expect(formatSize(0)).toBe('—');
    expect(formatSize(undefined)).toBe('—');
    expect(formatSize(null)).toBe('—');
  });

  it('字节', () => {
    expect(formatSize(1)).toBe('1 B');
    expect(formatSize(1023)).toBe('1023 B');
  });

  it('KB', () => {
    expect(formatSize(1024)).toBe('1.0 KB');
    expect(formatSize(1536)).toBe('1.5 KB');
  });

  it('MB', () => {
    expect(formatSize(1024 * 1024)).toBe('1.0 MB');
    expect(formatSize(3 * 1024 * 1024)).toBe('3.0 MB');
  });
});

describe('formatDateTime / formatTime', () => {
  // 用固定时区的本日时间戳，断言里只看结构不硬编码日期，
  // 避免测试在别的时区或日期跑挂。
  it('空值显示占位符', () => {
    expect(formatDateTime(0)).toBe('');
    expect(formatTime(0)).toBe('—');
    expect(formatTime(null)).toBe('—');
  });

  it('formatTime 补零到 HH:MM:SS', () => {
    // 用本地时区的今天 01:02:03
    const d = new Date();
    d.setHours(1, 2, 3, 0);
    expect(formatTime(d.getTime())).toBe('01:02:03');
  });

  it('formatDateTime 输出 月/日 时:分 结构', () => {
    const d = new Date();
    d.setHours(9, 5, 0, 0);
    const got = formatDateTime(d.getTime());
    expect(got).toMatch(/^\d{1,2}\/\d{1,2} 09:05$/);
  });
});
