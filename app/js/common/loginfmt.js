// loginfmt.js — OAuth ログインまわりの純粋関数（DOM 非依存。単体テスト可能）。
// ログインは Redmine の OAuth 2.0（Design.md §3）。失敗は `#login?error=<code>`
// でサーバーから戻ってくる（§3.4）。

const ERROR_HASH = /^#login\?error=([a-z_]{1,64})$/;

// loginErrorFromHash は `#login?error=<code>` のコードを返す。それ以外は ''。
// 任意の文字列を画面に反映しないよう、小文字と _ だけの短いコードに限る。
export function loginErrorFromHash(hash) {
  if (typeof hash !== 'string') return '';
  const m = ERROR_HASH.exec(hash);
  return m ? m[1] : '';
}

const MESSAGES = {
  access_denied: 'Redmine で許可されなかったため、ログインできませんでした。アプリを使うには、Redmine の画面で許可してください。',
  invalid_state: 'ログインの手続きが途中で無効になりました（時間切れ、または別のブラウザからの戻り）。もう一度最初からログインしてください。',
  exchange_failed: 'Redmine との認証を完了できませんでした。もう一度ログインしてください。',
  redmine_unavailable: 'Redmine に接続できませんでした。時間をおいて再試行してください。',
  server_misconfigured: 'このアプリの Redmine への登録に不備があります。管理者に連絡してください。',
  rate_limited: '試行回数が多すぎます。しばらく待ってからやり直してください。',
  server_error: 'サーバーでエラーが発生しました。時間をおいて再試行してください。',
};

const FALLBACK = 'ログインに失敗しました。もう一度お試しください。';

export function loginErrorMessage(code) {
  return MESSAGES[code] || FALLBACK;
}

// サーバー（internal/auth の戻り先の許可リスト）と同じ集合。クライアント側で先に
// 絞っておくが、最終的な検証はサーバーが行う。
const RETURN_HASH = /^#(projects|issues|settings|issues\/[0-9]{1,9}|issue-detail\/[0-9]{1,9})$/;

// returnHashFor はログイン後に戻る画面ハッシュ。許可外は ''（サーバー既定へ）。
export function returnHashFor(hash) {
  return typeof hash === 'string' && RETURN_HASH.test(hash) ? hash : '';
}

// loginUrl は GET /api/auth/login への遷移先。fetch ではなくページ遷移で使う。
export function loginUrl(hash) {
  const ret = returnHashFor(hash);
  return '/api/auth/login' + (ret ? `?return=${encodeURIComponent(ret)}` : '');
}
