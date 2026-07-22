// modal-issue-create.js — チケット作成モーダル（#modal-issue-create/<projectId>、
// Design.md §7.7 の FAB から開く）。トラッカー・優先度は /api/meta のマスタから
// 選ばせる（表示名の直書き禁止）。担当者はチケット詳細と同じ理由で対象外
// （メンバー取得が必要。将来対応）。

import { apiGetJson, apiPostJson, ApiError } from '../common/api.js';
import { closeModal } from '../common/modal.js';
import { validateIssueCreate, issueCreatePayload } from '../common/issuefmt.js';
import { escapeHtml, errorMessage } from '../common/utils.js';
import { toast } from '../common/shell.js';

export async function initModalIssueCreate(container, params) {
  const projectId = params && params[0];
  const form = container.querySelector('#issueCreateForm');
  const trackerSel = container.querySelector('#createTracker');
  const prioritySel = container.querySelector('#createPriority');
  const subjectInput = container.querySelector('#createSubject');
  const descInput = container.querySelector('#createDescription');
  const errorBox = container.querySelector('#issueCreateError');
  const submitBtn = container.querySelector('#issueCreateSubmit');
  if (!form) return;

  for (const btn of [container.querySelector('#issueCreateClose'), container.querySelector('#issueCreateCancel')]) {
    if (btn) btn.addEventListener('click', () => closeModal());
  }

  function showError(msg) {
    errorBox.textContent = msg;
    errorBox.hidden = !msg;
  }

  try {
    const meta = await apiGetJson('/api/meta');
    setOptions(trackerSel, (meta.trackers || []).map((t) => ({ value: String(t.id), label: t.name })));
    setOptions(prioritySel, [
      { value: '', label: '既定' },
      ...(meta.priorities || []).map((p) => ({ value: String(p.id), label: p.name })),
    ]);
  } catch (e) {
    showError(messageFor(e));
    submitBtn.disabled = true;
    return;
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fields = {
      projectId,
      trackerId: trackerSel.value,
      subject: subjectInput.value,
      priorityId: prioritySel.value,
      description: descInput.value,
    };
    const errors = validateIssueCreate(fields);
    if (Object.keys(errors).length > 0) {
      showError(Object.values(errors)[0]);
      return;
    }
    showError('');
    submitBtn.disabled = true;
    try {
      const res = await apiPostJson('/api/redmine/issues.json', issueCreatePayload(fields));
      const newId = res && res.issue && res.issue.id;
      toast('チケットを作成しました', 'ok');
      location.hash = newId ? `#issue-detail/${newId}` : `#issues/${encodeURIComponent(projectId)}`;
    } catch (err) {
      showError(messageFor(err));
      submitBtn.disabled = false;
    }
  });
}

function setOptions(sel, opts) {
  sel.innerHTML = opts
    .map((o) => `<option value="${escapeHtml(o.value)}">${escapeHtml(o.label)}</option>`)
    .join('');
}

function messageFor(e) {
  if (e instanceof ApiError && e.code) return errorMessage(e.code);
  return 'チケットの作成に失敗しました。時間をおいて再試行してください。';
}
