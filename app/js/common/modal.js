// modal.js — ハッシュ（#modal-<key>）で開閉するモーダルの最小ラッパー。
// 画面フラグメントと同様、モーダルの中身は呼び出し側が描画する。ここでは
// 表示・非表示・フォーカストラップ・Esc 閉じ・背景クリック閉じだけを担う。

let activeClose = null;

// openModal は container 要素を <dialog> 的に前面表示する。onClose は閉じた
// ときに一度だけ呼ばれる。戻り値の関数でプログラム的に閉じられる。
export function openModal(container, { onClose } = {}) {
  closeModal(); // 同時に開くのは 1 つ

  const overlay = document.createElement('div');
  overlay.className = 'modal-overlay';
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-modal', 'true');
  if (!container.hasAttribute('tabindex')) container.setAttribute('tabindex', '-1');
  overlay.appendChild(container);
  document.body.appendChild(overlay);
  document.body.classList.add('modal-open');

  const prevFocus = document.activeElement;

  const close = () => {
    if (activeClose !== close) return;
    activeClose = null;
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    document.body.classList.remove('modal-open');
    if (prevFocus && typeof prevFocus.focus === 'function') prevFocus.focus();
    if (onClose) onClose();
  };
  // onKey は Esc で閉じ、Tab はモーダル内でフォーカスを循環させる（フォーカストラップ）。
  const onKey = (e) => {
    if (e.key === 'Escape') {
      close();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = focusableItems(container);
    if (items.length === 0) {
      e.preventDefault();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const cur = document.activeElement;
    if (e.shiftKey && (cur === first || !container.contains(cur))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (cur === last || !container.contains(cur))) {
      e.preventDefault();
      first.focus();
    }
  };
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });
  document.addEventListener('keydown', onKey);

  activeClose = close;
  // 最初のフォーカス可能要素へ（disabled / hidden は除外）。
  const items = focusableItems(container);
  if (items.length > 0) items[0].focus();
  else container.focus();
  return close;
}

// focusableItems は container 内の実際にフォーカス可能な要素を文書順で返す
//（disabled・非表示・tabindex=-1 を除外）。
function focusableItems(container) {
  const sel = 'a[href], input, button, textarea, select, [tabindex]';
  return Array.from(container.querySelectorAll(sel)).filter((el) => {
    if (el.disabled) return false;
    if (el.getAttribute('tabindex') === '-1') return false;
    if (el.hidden) return false;
    // 非表示（display:none 等）は offsetParent が null。fixed 要素は例外だが
    // モーダル内容には十分。
    return el.offsetParent !== null || el === document.activeElement;
  });
}

// closeModal は現在開いているモーダルを閉じる（無ければ何もしない）。
export function closeModal() {
  if (activeClose) activeClose();
}

// isModalHash はハッシュがモーダルルートかを判定する（`#modal-<key>` に続く
// `/<param>/...` も許可する。issue-create モーダルの projectId など）。
export function isModalHash(hash) {
  return /^#modal-[a-z0-9-]+(\/.*)?$/i.test(hash || '');
}
