// settingsfmt.js — 設定画面（Design.md §7.9）の純粋な整形ヘルパー
//（DOM に触れない。単体テスト可能）。

// redmineStatusInfo は Redmine 連携状態（"active"/"invalid"/"unlinked" と
// それ以外すべて）を利用者向けラベルとバッジ種別に写像する。
export function redmineStatusInfo(status) {
  if (status === 'active') return { label: '連携済み', kind: 'ok' };
  if (status === 'invalid') return { label: '要再認可', kind: 'crit' };
  return { label: '未連携', kind: 'warn' };
}

// needsReauthorization は再認可（Redmine での許可のやり直し）が必要な状態か。
export function needsReauthorization(status) {
  return status !== 'active';
}

// scopesSummary は付与スコープの表示用リスト（空文字・文字列以外は捨てる）。
export function scopesSummary(scopes) {
  if (!Array.isArray(scopes)) return [];
  return scopes.filter((s) => typeof s === 'string' && s !== '');
}
