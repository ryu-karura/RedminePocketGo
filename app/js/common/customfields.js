// customfields.js — カスタムフィールドの表示整形（純粋関数。DOM 非依存）。
// サーバー（/api/issues/{id}/detail）が定義との突合・選択肢ラベル解決・
// version/user/attachment の参照解決までを済ませて返すため（Design.md
// §6.4）、ここでは残りの表示整形（真偽値のラベル化、長いテキストの改行
// 保持、リンクのアンカー化、複数値の連結）だけを行う（Design.md §7.8）。

// rawValuesOf は value（文字列 / 複数選択の配列 / null）を文字列配列へ
// 正規化する。
function rawValuesOf(value) {
  if (value == null || value === '') return [];
  if (Array.isArray(value)) {
    return value.filter((v) => v != null && v !== '').map(String);
  }
  return [String(value)];
}

function boolLabel(value) {
  const raw = Array.isArray(value) ? value[0] : value;
  if (raw === true || raw === 1 || raw === '1') return 'はい';
  if (raw === false || raw === 0 || raw === '0') return 'いいえ';
  return '未設定';
}

// formatCustomFieldValue はカスタムフィールド 1 件の表示情報を返す。
// kind は画面側の描画方法（'multiline' は改行保持、'link' はアンカー、
// それ以外はテキスト）。href は kind:'link' のときだけ入る。
export function formatCustomFieldValue(field) {
  const f = field || {};
  const format = f.field_format || '';

  if (format === 'bool') {
    return { kind: 'text', text: boolLabel(f.value) };
  }
  // list / key_value_list / version / user / attachment はサーバーが
  // 解決済みの表示名を display_value に入れる（degrade 時は空）。
  if (f.display_value) {
    return { kind: 'text', text: f.display_value };
  }
  if (format === 'link') {
    const raw = rawValuesOf(f.value)[0];
    if (!raw) return { kind: 'text', text: '—' };
    // http/https 以外（javascript: 等）はアンカー化しない。値は Redmine の
    // 利用者が自由に入力できるため、そのままリンク化すると XSS になりうる。
    if (!/^https?:\/\//i.test(raw)) return { kind: 'text', text: raw };
    return { kind: 'link', text: raw, href: raw };
  }
  if (format === 'text') {
    const joined = rawValuesOf(f.value).join('\n');
    return { kind: 'multiline', text: joined };
  }
  const joined = rawValuesOf(f.value).join('、');
  return { kind: 'text', text: joined || '—' };
}

// requiredLabel は必須バッジの文言（未必須は空文字で非表示にする）。
export function requiredLabel(field) {
  return field && field.is_required ? '必須' : '';
}
