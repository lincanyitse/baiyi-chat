// 展示用的格式化函数。多个管理面板都要这些，放一处避免各写一份
// 略有出入的版本（比如有的补零有的不补）。

function pad(n) {
  return String(n).padStart(2, '0');
}

/** 把毫秒时间戳格式化成「月/日 时:分」。 */
export function formatDateTime(ms) {
  if (!ms) return '';
  const d = new Date(ms);
  return `${d.getMonth() + 1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 把毫秒时间戳格式化成「时:分:秒」，用于同一天内的轮次列表。 */
export function formatTime(ms) {
  if (!ms) return '—';
  const d = new Date(ms);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** 把字节数格式化成人类可读的大小。 */
export function formatSize(n) {
  if (!n) return '—';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(1) + ' MB';
}
