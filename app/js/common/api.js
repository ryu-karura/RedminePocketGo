// api.js — すべての HTTP はここを通す（画面が fetch を直接呼ぶのは禁止。
// CLAUDE.md §3.2）。更新系（POST/PUT/PATCH/DELETE）には CSRF 対策の
// `X-Requested-With: XMLHttpRequest` を必ず付与する（サーバーは無いと拒否）。

// ApiError はエラーエンベロープ（{ error: { code, message } }）を包む。
export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code || '';
  }
}

async function request(method, path, body) {
  const headers = {};
  const opts = { method, headers, credentials: 'same-origin' };

  const isWrite = method !== 'GET' && method !== 'HEAD';
  if (isWrite) headers['X-Requested-With'] = 'XMLHttpRequest';
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }

  let resp;
  try {
    resp = await fetch(path, opts);
  } catch (e) {
    throw new ApiError(0, 'network_error', String(e && e.message ? e.message : e));
  }

  const text = await resp.text();
  const data = text ? safeParse(text) : null;

  if (!resp.ok) {
    const env = data && data.error ? data.error : {};
    // Redmine との連携が使えなくなった（トークンの失効・取り消し）。画面ごとに
    // 扱わず、アプリ全体で再認可の案内を出す（app.js が購読する。Design.md §7.5）。
    if (resp.status === 409 && env.code === 'redmine_credential_invalid' && typeof window !== 'undefined') {
      window.dispatchEvent(new CustomEvent('rmapp:reauth-required'));
    }
    throw new ApiError(resp.status, env.code, env.message);
  }
  return data;
}

function safeParse(text) {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export const apiGetJson = (path) => request('GET', path);
export const apiPostJson = (path, body) => request('POST', path, body ?? {});
export const apiPutJson = (path, body) => request('PUT', path, body ?? {});
export const apiPatchJson = (path, body) => request('PATCH', path, body ?? {});
export const apiDeleteJson = (path) => request('DELETE', path);
