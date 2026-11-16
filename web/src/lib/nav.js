// 设置页的分组导航。
//
// 10 个平铺文字 tab 让能力全都挤在同一层，用户既找不到也分不清。
// 这里给每个 tab 加上分组、图标与一句说明，同一份常量同时供宽屏
// 竖导航、窄屏横向 chip 和能力概览的深链使用。

/** 分组定义。顺序即展示顺序。 */
export const navGroups = [
  { key: 'overview', label: '概览与用量' },
  { key: 'intelligence', label: '智能与工具' },
  { key: 'system', label: '系统与数据' },
];

/**
 * 每个条目：key 是 AdminPanel 的 tab 值；class 写全，不做字符串拼接，
 * 否则 UnoCSS 的静态提取会漏掉动态类名。
 */
export const navItems = [
  {
    key: 'capabilities', label: '能力一览', icon: 'sparkle', group: 'overview',
    desc: '白泽能做什么，以及当前装备了什么',
  },
  {
    key: 'cost', label: '用量', icon: 'chart', group: 'overview',
    desc: 'token 用量与花费，按模型拆分',
  },
  {
    key: 'providers', label: '模型服务', icon: 'sparkle', group: 'intelligence',
    desc: '配置渠道商、密钥、模型与价格',
  },
  {
    key: 'skills', label: '技能', icon: 'book', group: 'intelligence',
    desc: '按需加载的 Markdown 能力包',
  },
  {
    key: 'mcp', label: '插件', icon: 'tool', group: 'intelligence',
    desc: '接入外部工具服务（MCP server）',
  },
  {
    key: 'kb', label: '知识库', icon: 'database', group: 'intelligence',
    desc: '导入文档，让回答带上可点击的引用',
  },
  {
    key: 'memory', label: '记忆', icon: 'brain', group: 'intelligence',
    desc: '跨会话记住的偏好与事实，可追溯可撤销',
  },
  {
    key: 'context', label: '上下文', icon: 'layers', group: 'intelligence',
    desc: '查看每一轮模型实际看到了什么、为什么被裁剪',
  },
  {
    key: 'data', label: '数据', icon: 'archive', group: 'system',
    desc: '导出会话、备份与恢复',
  },
  {
    key: 'security', label: '安全', icon: 'shield', group: 'system',
    desc: '访问口令',
  },
  {
    key: 'about', label: '关于', icon: 'info', group: 'system',
    desc: '版本、路径、诊断与外观设置',
  },
];

export function itemsOfGroup(group) {
  return navItems.filter((it) => it.group === group);
}
