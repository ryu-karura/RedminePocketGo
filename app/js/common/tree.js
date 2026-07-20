// tree.js — Redmine の親子データを Tabulator dataTree 用に整形する純粋関数群。
// サーバーの集約 API（/api/projects/tree 等）は入れ子の `children` を返すので、
// ここでは Tabulator が期待する `_children` へ変換し、検索時の祖先自動展開など
// 表示ロジックに必要な純粋変換を提供する。DOM に触れない（単体テスト可能）。

const CHILDREN = 'children';

// toDataTree は `children` 配列を Tabulator の `_children` に付け替える。
// 葉（子なし）には `_children` を付けない（展開矢印を出さないため）。
// 入力は破壊しない。
export function toDataTree(nodes, childrenKey = CHILDREN) {
  return (nodes || []).map((n) => {
    const kids = n[childrenKey] || [];
    const { [childrenKey]: _omit, ...rest } = n;
    if (kids.length === 0) return rest;
    return { ...rest, _children: toDataTree(kids, childrenKey) };
  });
}

// flatten は木を先行順の平坦配列にする。
export function flatten(nodes, childrenKey = CHILDREN) {
  const out = [];
  const walk = (ns) => {
    for (const n of ns || []) {
      out.push(n);
      walk(n[childrenKey]);
    }
  };
  walk(nodes);
  return out;
}

// filterTree は述語にマッチするノードと、その祖先だけを残した新しい木を返す。
// マッチしたノードの子孫はすべて保持する（絞り込み結果の文脈を見せるため）。
export function filterTree(nodes, pred, childrenKey = CHILDREN) {
  const walk = (ns) => {
    const kept = [];
    for (const n of ns || []) {
      if (pred(n)) {
        kept.push(n); // マッチ: 子孫はそのまま
        continue;
      }
      const keptChildren = walk(n[childrenKey]);
      if (keptChildren.length > 0) {
        kept.push({ ...n, [childrenKey]: keptChildren });
      }
    }
    return kept;
  };
  return walk(nodes);
}

// collectMatchAncestors は述語にマッチするノードの祖先 id 集合を返す
//（検索時に自動展開すべきノード）。マッチ自身は含めない。
export function collectMatchAncestors(nodes, pred, childrenKey = CHILDREN) {
  const ids = new Set();
  const walk = (ns, ancestors) => {
    let found = false;
    for (const n of ns || []) {
      const childFound = walk(n[childrenKey], [...ancestors, n]);
      const self = pred(n);
      if (self || childFound) {
        if (childFound) {
          for (const a of ancestors) ids.add(a.id);
          ids.add(n.id);
        } else if (self) {
          for (const a of ancestors) ids.add(a.id);
        }
        found = true;
      }
    }
    return found;
  };
  walk(nodes, []);
  return ids;
}
