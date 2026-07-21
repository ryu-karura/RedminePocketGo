// utils.js — 画面共通の日付・書式・分類ヘルパー（純粋関数。DOM に触れない）。
// 画面ごとに同じロジックを再実装しない（CLAUDE.md §3.2）。

const JST_OFFSET_MIN = 9 * 60; // +09:00 固定（このアプリの表示タイムゾーン）

// daysUntil は due（YYYY-MM-DD）までの残り日数（整数）を返す。
// 過去は負、当日は 0。未指定は null。
export function daysUntil(due, now = new Date()) {
  if (!due) return null;
  const d = new Date(`${due}T00:00:00+09:00`);
  if (Number.isNaN(d.getTime())) return null;
  const today = jstMidnight(now);
  return Math.round((d.getTime() - today) / 86400000);
}

function jstMidnight(now) {
  // now を JST の 0 時に丸めた epoch(ms)。now.getTime() は絶対時刻（UTC）で
  // 実行環境のタイムゾーンに依存しないので、そのまま +9h してから丸める。
  // （getTimezoneOffset を足すと実行機のローカル TZ に依存し 1 日ずれる）。
  const jstShifted = now.getTime() + JST_OFFSET_MIN * 60000;
  const d = new Date(jstShifted);
  d.setUTCHours(0, 0, 0, 0);
  return d.getTime() - JST_OFFSET_MIN * 60000;
}

// dueDateSeverity は期日の逼迫度を返す（Design.md §7.4）。
// 8 日以上先=ok、7 日以内（当日含む）=warn、超過=crit。未指定は null。
export function dueDateSeverity(due, now = new Date()) {
  const n = daysUntil(due, now);
  if (n === null) return null;
  if (n < 0) return 'crit';
  if (n <= 7) return 'warn';
  return 'ok';
}

// statusKind は Redmine ステータスを new / open / closed に分類する
//（Design.md §7.4。固定表は持たず is_closed と並び順から決める）。
export function statusKind(status, allStatuses) {
  const list = allStatuses || [];
  const s = list.find((x) => x.id === status.id);
  if (!s) return 'open';
  if (s.is_closed) return 'closed';
  const firstOpen = list.find((x) => !x.is_closed);
  if (firstOpen && firstOpen.id === s.id) return 'new';
  return 'open';
}

// priorityKind は優先度を low / normal / high / urgent に分類する
//（並び順で判定。最下位=low、2 番目=normal、以降 high、最上位=urgent）。
export function priorityKind(priority, allPriorities) {
  const list = allPriorities || [];
  const i = list.findIndex((x) => x.id === priority.id);
  if (i < 0) return 'normal';
  if (i === list.length - 1) return 'urgent';
  if (i === 0) return 'low';
  if (i === 1) return 'normal';
  return 'high';
}

// escapeHtml は文字列を HTML テキストとして安全に埋め込める形に変換する。
export function escapeHtml(s) {
  if (s == null) return '';
  return String(s)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

// formatDateTime は ISO8601 を +09:00 の ISO8601 表記に整える（Design.md §3.4 の
// タイムゾーン明示方針）。
export function formatDateTime(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const jst = new Date(d.getTime() + JST_OFFSET_MIN * 60000);
  const p = (n, w = 2) => String(n).padStart(w, '0');
  return `${jst.getUTCFullYear()}-${p(jst.getUTCMonth() + 1)}-${p(jst.getUTCDate())}` +
    `T${p(jst.getUTCHours())}:${p(jst.getUTCMinutes())}:${p(jst.getUTCSeconds())}+09:00`;
}

// errorMessage はエラーエンベロープの code を利用者向け日本語に写像する
//（Design.md §6.5: 表示文言は SPA が code から決める）。
const MESSAGES = {
  unauthenticated: 'セッションの期限が切れました。もう一度ログインしてください。',
  forbidden: 'この操作を行う権限がありません。',
  not_found: '対象が見つかりませんでした。',
  invalid_request: '入力内容を確認してください。',
  redmine_credential_invalid: 'Redmine の API キーが無効です。再度連携してください。',
  upstream_error: 'Redmine に接続できませんでした。時間をおいて再試行してください。',
  rate_limited: '試行回数が多すぎます。しばらく待ってからやり直してください。',
  internal_error: 'サーバーでエラーが発生しました。',
};

export function errorMessage(code) {
  return MESSAGES[code] || 'エラーが発生しました。時間をおいて再試行してください。';
}
