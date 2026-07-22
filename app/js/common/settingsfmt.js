// settingsfmt.js — 設定画面（Design.md §7.9）の純粋な整形ヘルパー
//（DOM に触れない。単体テスト可能）。

// deviceLabel は端末の表示名（未設定はプレースホルダ）。
export function deviceLabel(device) {
  return (device && device.label) || '(表示名未設定)';
}

// deviceKindLabel は端末種別（未設定は「不明」）。
export function deviceKindLabel(device) {
  return (device && device.kind) || '不明';
}

// redmineStatusInfo は Redmine 連携状態（"active"/"invalid"/"unlinked" と
// それ以外すべて）を利用者向けラベルとバッジ種別に写像する。
export function redmineStatusInfo(status) {
  if (status === 'active') return { label: '連携済み', kind: 'ok' };
  if (status === 'invalid') return { label: '要再連携', kind: 'crit' };
  return { label: '未連携', kind: 'warn' };
}
