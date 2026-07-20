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
  overlay.appendChild(container);
  document.body.appendChild(overlay);
  document.body.classList.add('modal-open');

  const close = () => {
    if (activeClose !== close) return;
    activeClose = null;
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    document.body.classList.remove('modal-open');
    if (onClose) onClose();
  };
  const onKey = (e) => {
    if (e.key === 'Escape') close();
  };
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });
  document.addEventListener('keydown', onKey);

  activeClose = close;
  // 最初のフォーカス可能要素へ
  const focusable = container.querySelector(
    'input, button, textarea, select, [tabindex]',
  );
  if (focusable) focusable.focus();
  return close;
}

// closeModal は現在開いているモーダルを閉じる（無ければ何もしない）。
export function closeModal() {
  if (activeClose) activeClose();
}

// isModalHash はハッシュがモーダルルートかを判定する。
export function isModalHash(hash) {
  return /^#modal-[a-z0-9-]+$/i.test(hash || '');
}
