// shell.js — 共通シェルの振る舞い: ドロワーナビ、テーマ切替、トースト、
// ログインオーバーレイの開閉。画面遷移そのものは app.js が担う。

// ---- テーマ ----

export function initTheme() {
  const btn = document.getElementById('themeBtn');
  if (btn) {
    btn.addEventListener('click', () => {
      const dark = document.documentElement.classList.toggle('dark');
      try { localStorage.setItem('theme', dark ? 'dark' : 'light'); } catch (e) {}
    });
  }
}

// ---- ドロワー ----

export function initDrawer(screens, onNavigate) {
  const drawer = document.getElementById('drawer');
  const backdrop = document.getElementById('drawerBackdrop');
  const menuBtn = document.getElementById('menuBtn');

  drawer.innerHTML = '';
  for (const s of screens) {
    if (s.hidden) continue;
    const a = document.createElement('a');
    a.className = 'drawer__link';
    a.href = `#${s.key}`;
    a.textContent = s.label;
    a.dataset.key = s.key;
    a.addEventListener('click', () => closeDrawer());
    drawer.appendChild(a);
  }

  const open = () => {
    drawer.classList.add('open');
    backdrop.classList.add('open');
    menuBtn.setAttribute('aria-expanded', 'true');
  };
  const closeDrawer = () => {
    drawer.classList.remove('open');
    backdrop.classList.remove('open');
    menuBtn.setAttribute('aria-expanded', 'false');
  };
  menuBtn.addEventListener('click', () => {
    if (drawer.classList.contains('open')) closeDrawer(); else open();
  });
  backdrop.addEventListener('click', closeDrawer);
  if (onNavigate) onNavigate();
}

// setActiveNav は現在の画面キーをドロワーに反映する。
export function setActiveNav(key) {
  for (const a of document.querySelectorAll('.drawer__link')) {
    if (a.dataset.key === key) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
}

// setTitle はトップバーの見出しを更新する。
export function setTitle(text) {
  const el = document.getElementById('topbarTitle');
  if (el) el.textContent = text || 'RedminePocketGo';
}

// ---- トースト ----

export function toast(message, kind = '') {
  const host = document.getElementById('toasts');
  if (!host) return;
  const el = document.createElement('div');
  el.className = `toast ${kind}`.trim();
  el.setAttribute('role', 'status');
  el.textContent = message;
  host.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

// ---- ログインオーバーレイ ----

export function showLogin(node) {
  const overlay = document.getElementById('login-overlay');
  overlay.innerHTML = '';
  overlay.appendChild(node);
  overlay.classList.add('active');
}

export function hideLogin() {
  const overlay = document.getElementById('login-overlay');
  overlay.classList.remove('active');
  overlay.innerHTML = '';
}
