// issuefmt.js — チケット一覧・詳細で使う純粋な分類・整形ヘルパー
//（DOM に触れない。単体テスト可能。Design.md §7.7）。バッジの色分類は
// utils の statusKind / priorityKind に委譲する（固定表を持たない）。

import { statusKind, priorityKind } from './utils.js';
import { flatten } from './tree.js';

// assigneeLabel は担当者名（未割り当ては「担当なし」）。
export function assigneeLabel(issue) {
  return (issue && issue.assigned_to && issue.assigned_to.name) || '担当なし';
}

// countClosed はツリー全体（子孫含む）の完了チケット数を返す。
export function countClosed(tree, statuses) {
  return flatten(tree).filter((i) => statusKind(i.status || {}, statuses) === 'closed').length;
}

// pruneIssues は keep(node) が真のノードだけ残したツリーを返す。落としたノード
// の残る子孫は上位へ引き上げる（絞り込みで親が外れても子は見えるようにする）。
export function pruneIssues(tree, keep) {
  const walk = (nodes) => {
    const out = [];
    for (const n of nodes || []) {
      const keptChildren = walk(n.children);
      if (keep(n)) out.push({ ...n, children: keptChildren });
      else out.push(...keptChildren);
    }
    return out;
  };
  return walk(tree);
}

// filterOpen は完了チケットを畳んだツリーを返す（完了は取り除き、未完了の子孫を
// 昇格。pruneIssues の状態版）。
export function filterOpen(tree, statuses) {
  return pruneIssues(tree, (i) => statusKind(i.status || {}, statuses) !== 'closed');
}

// issueBadges は 1 行に出すバッジ情報（状態・優先度・担当）をまとめて返す。
// kind は CSS クラス（status-new など）に対応する分類キー。
export function issueBadges(issue, meta) {
  const statuses = (meta && meta.statuses) || [];
  const priorities = (meta && meta.priorities) || [];
  return {
    status: {
      kind: statusKind(issue.status || {}, statuses),
      label: (issue.status && issue.status.name) || '',
    },
    priority: {
      kind: priorityKind(issue.priority || {}, priorities),
      label: (issue.priority && issue.priority.name) || '',
    },
    assignee: assigneeLabel(issue),
  };
}

// issuePatch は元のチケットと編集値から、Redmine 更新用の最小ボディ
// `{ issue: {...変更項目のみ...} }` を作る（Design.md §7.8「変更した項目だけを
// 送信」）。変更がなければ null。notes は空でなければ常に含める（コメント追加）。
export function issuePatch(original, changes) {
  const o = original || {};
  const issue = {};
  const differs = (v, cur) => v != null && String(v) !== String(cur == null ? '' : cur);

  if (differs(changes.statusId, o.status && o.status.id)) {
    issue.status_id = Number(changes.statusId);
  }
  if (differs(changes.priorityId, o.priority && o.priority.id)) {
    issue.priority_id = Number(changes.priorityId);
  }
  if (changes.doneRatio != null && Number(changes.doneRatio) !== Number(o.done_ratio || 0)) {
    issue.done_ratio = Number(changes.doneRatio);
  }
  if (differs(changes.assignedToId, o.assigned_to && o.assigned_to.id)) {
    issue.assigned_to_id = Number(changes.assignedToId);
  }
  if (changes.notes != null && String(changes.notes).trim() !== '') {
    issue.notes = String(changes.notes);
  }
  return Object.keys(issue).length ? { issue } : null;
}

// matchIssue は絞り込み条件（状態種別・優先度種別・担当者 id）に対する述語。
// 空（null/undefined/''）の条件は無視する。status は 'open'|'closed'|null。
export function matchIssue(issue, meta, { status, priorityId, assigneeId } = {}) {
  const statuses = (meta && meta.statuses) || [];
  if (status) {
    const closed = statusKind(issue.status || {}, statuses) === 'closed';
    if (status === 'closed' && !closed) return false;
    if (status === 'open' && closed) return false;
  }
  if (priorityId != null && priorityId !== '') {
    if (!issue.priority || String(issue.priority.id) !== String(priorityId)) return false;
  }
  if (assigneeId != null && assigneeId !== '') {
    const aid = issue.assigned_to ? String(issue.assigned_to.id) : '0'; // 0 = 担当なし
    if (aid !== String(assigneeId)) return false;
  }
  return true;
}
