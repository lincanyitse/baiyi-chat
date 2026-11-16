// HTTP 客户端：统一错误处理与 JSON 编解码。

/** API 错误，带后端返回的错误码。 */
export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

/**
 * 401 的处理钩子，由 App 注册。
 *
 * 用回调而不是在这里 import store：api.js 是底层模块，
 * 反向依赖状态层会形成循环引用，也会让测试难写。
 */
let onUnauthorized = null;

/** 注册 401 回调。传 null 取消。 */
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

/**
 * 发起请求并解析 JSON。
 *
 * 后端统一用 {error:{code,message}} 表达失败，这里解出来抛
 * ApiError，让 UI 能按 code 区分「会话不存在」与「服务出错」。
 */
export async function request(method, path, body) {
  const opts = {
    method,
    headers: {},
  };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }

  let resp;
  try {
    resp = await fetch(path, opts);
  } catch (err) {
    throw new ApiError(0, 'network', '网络不可用：' + err.message);
  }

  if (resp.status === 204) return null;

  const text = await resp.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new ApiError(resp.status, 'bad_response', '响应不是合法 JSON');
    }
  }

  if (!resp.ok) {
    const e = data && data.error ? data.error : {};
    // 口令会话过期：通知上层重新走认证门，而不是让用户对着一行
    // 「未登录」红字发呆。登录接口本身除外——那里 401 就是口令错。
    if (resp.status === 401 && !path.startsWith('/api/auth/') && onUnauthorized) {
      onUnauthorized();
    }
    throw new ApiError(resp.status, e.code || 'unknown', e.message || `HTTP ${resp.status}`);
  }
  return data;
}

/**
 * 以 multipart 上传单个文件。
 *
 * 不能复用 request()：那里固定发 JSON，而 FormData 必须让浏览器
 * 自己设置带 boundary 的 Content-Type。
 */
async function uploadFile(path, file) {
  const form = new FormData();
  form.append('file', file);

  let resp;
  try {
    resp = await fetch(path, { method: 'POST', body: form });
  } catch (err) {
    throw new ApiError(0, 'network', '网络不可用：' + err.message);
  }

  const text = await resp.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new ApiError(resp.status, 'bad_response', '响应不是合法 JSON');
    }
  }

  if (!resp.ok) {
    const e = data?.error || {};
    if (resp.status === 401 && onUnauthorized) onUnauthorized();
    throw new ApiError(resp.status, e.code || 'unknown', e.message || `HTTP ${resp.status}`);
  }
  return data;
}

export const api = {
  // 不带参数时返回数组（兼容旧形状）；带 q 或 offset 时返回 {items,total}
  listSessions: ({ q = '', offset = 0, limit = 100, includeArchived = false } = {}) => {
    const params = new URLSearchParams();
    if (q) params.set('q', q);
    if (offset) params.set('offset', String(offset));
    if (limit !== 100) params.set('limit', String(limit));
    if (includeArchived) params.set('include_archived', 'true');
    const qs = params.toString();
    return request('GET', `/api/sessions${qs ? '?' + qs : ''}`);
  },
  createSession: (title) => request('POST', '/api/sessions', { title }),
  updateSession: (id, patch) => request('PATCH', `/api/sessions/${id}`, patch),
  deleteSession: (id) => request('DELETE', `/api/sessions/${id}`),
  messages: (id, leaf) =>
    request('GET', `/api/sessions/${id}/messages${leaf ? `?branch=${leaf}` : ''}`),
  tree: (id, leaf) =>
    request('GET', `/api/sessions/${id}/tree${leaf ? `?branch=${leaf}` : ''}`),

  startTurn: (sessionId, text, attachmentIds = []) =>
    request('POST', `/api/sessions/${sessionId}/turns`, {
      text,
      attachment_ids: attachmentIds,
    }),
  turnStatus: (turnId) => request('GET', `/api/turns/${turnId}`),
  stopTurn: (turnId) => request('POST', `/api/turns/${turnId}/stop`),
  resumeTurn: (turnId) => request('POST', `/api/turns/${turnId}/resume`),

  regenerate: (messageId) => request('POST', `/api/messages/${messageId}/regenerate`),
  editMessage: (messageId, text) =>
    request('PATCH', `/api/messages/${messageId}`, { text }),
  siblings: (messageId) => request('GET', `/api/messages/${messageId}/siblings`),
  searchMessages: (sessionId, q, limit = 50) =>
    request(
      'GET',
      `/api/sessions/${encodeURIComponent(sessionId)}/search?q=${encodeURIComponent(q)}&limit=${limit}`
    ),

  pendingApprovals: () => request('GET', '/api/approvals/pending'),
  decideApproval: (id, action, reason = '') =>
    request('POST', `/api/approvals/${id}`, { action, reason }),

  authStatus: () => request('GET', '/api/auth/status'),
  login: (password) => request('POST', '/api/auth/login', { password }),
  logout: () => request('POST', '/api/auth/logout'),
  setupPassword: (password) => request('POST', '/api/auth/setup', { password }),
  changePassword: (oldPassword, newPassword) =>
    request('POST', '/api/auth/change', { old: oldPassword, new: newPassword }),

  saveProviders: (payload) => request('PUT', '/api/providers', payload),
  providerModels: (name) => request('GET', `/api/providers/${encodeURIComponent(name)}/models`),
  configuredModels: () => request('GET', '/api/models'),
  secretsStatus: () => request('GET', '/api/providers/secrets/status'),
  resetSecrets: () => request('POST', '/api/providers/secrets/reset'),

  skills: () => request('GET', '/api/skills'),
  setSkillEnabled: (name, enabled) =>
    request('PATCH', `/api/skills/${encodeURIComponent(name)}`, { enabled }),
  reloadSkills: () => request('POST', '/api/skills/reload'),

  mcp: () => request('GET', '/api/mcp'),
  saveMCP: (servers) => request('PUT', '/api/mcp', { servers }),
  reconnectMCP: (alias) =>
    request('POST', `/api/mcp/${encodeURIComponent(alias)}/reconnect`),

  backups: () => request('GET', '/api/backups'),
  createBackup: () => request('POST', '/api/backup'),
  system: () => request('GET', '/api/system'),
  health: () => request('GET', '/api/health'),
  resetData: (scope, confirm) => request('POST', '/api/reset', { scope, confirm }),
  diagnosticsURL: () => '/api/diagnostics',
  // 导出走浏览器下载，不经过 request()：它返回的是文件流不是 JSON
  exportSessionURL: (id, format) =>
    `/api/sessions/${encodeURIComponent(id)}/export?format=${format}`,
  exportAllURL: () => '/api/export?format=jsonl',

  contextSnapshots: (sessionId, limit = 20) =>
    request('GET', `/api/sessions/${sessionId}/context?limit=${limit}`),
  turnContext: (turnId, step) =>
    request('GET', `/api/turns/${turnId}/context${step != null ? `?step=${step}` : ''}`),
  turnContextDiff: (turnId) => request('GET', `/api/turns/${turnId}/context/diff`),

  memoryFacts: (status = '') =>
    request('GET', `/api/memory/facts${status ? `?status=${encodeURIComponent(status)}` : ''}`),
  factAction: (id, action, value = '') =>
    request('PATCH', `/api/memory/facts/${id}`, { action, value }),
  factEvents: (id) => request('GET', `/api/memory/facts/${id}/events`),
  memoryCard: () => request('GET', '/api/memory/card'),
  memoryStats: () => request('GET', '/api/memory/stats'),

  kbDocuments: ({ status = '', offset = 0, limit = 20 } = {}) => {
    const params = new URLSearchParams();
    if (status) params.set('status', status);
    if (offset) params.set('offset', String(offset));
    params.set('limit', String(limit));
    return request('GET', `/api/kb/documents?${params}`);
  },
  uploadDocument: (file) => uploadFile('/api/kb/documents', file),
  deleteDocument: (id) => request('DELETE', `/api/kb/documents/${id}`),
  documentChunks: (id) => request('GET', `/api/kb/documents/${id}/chunks`),
  kbSearch: (query, limit = 8) => request('POST', '/api/kb/search', { query, limit }),
  jobs: ({ kind = '', limit = 20 } = {}) => {
    const params = new URLSearchParams();
    if (kind) params.set('kind', kind);
    params.set('limit', String(limit));
    return request('GET', `/api/jobs?${params}`);
  },

  uploadAttachment: (file) => uploadFile('/api/attachments', file),
  attachmentURL: (id) => `/api/attachments/${id}`,
};
