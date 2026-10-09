// reauth.js — Redmine の再認可（OAuth トークンが無効になったときの入口）。
// サーバーの POST /api/auth/reauthorize が返す URL へ、fetch ではなくページ遷移
// する（Design.md §3.3, §4.4）。Redmine 側のセッションが生きていれば、同意の
// 確認だけで戻ってこられる。

import { apiPostJson } from './api.js';
import { returnHashFor } from './loginfmt.js';

// startReauthorize は再認可へ遷移する。失敗時は例外（呼び出し側が表示する）。
export async function startReauthorize(returnHash) {
  const ret = returnHashFor(returnHash);
  const res = await apiPostJson('/api/auth/reauthorize', ret ? { return: ret } : {});
  if (!res || typeof res.loginUrl !== 'string' || !res.loginUrl.startsWith('/')) {
    throw new Error('invalid reauthorize response');
  }
  location.assign(res.loginUrl);
}
