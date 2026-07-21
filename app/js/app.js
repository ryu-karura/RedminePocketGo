// app.js — ES モジュールのエントリ。SCREENS マニフェスト（全画面の唯一の
// 一覧）、ハッシュルーティング、起動時の GET /api/auth/me と認証ゲート。

import { apiGetJson, apiPostJson, ApiError } from './common/api.js';
import {
  initTheme, initDrawer, setActiveNav, setTitle, toast, showLogin, hideLogin,
} from './common/shell.js';
import { initLogin } from './screens/login.js';

// SCREENS: key / label / init。login はオーバーレイなのでナビには出さない。
// 業務画面（projects 以降）はフェーズ 6 で js/screens/<key>.js を実装する。
export const SCREENS = [
  { key: 'projects', label: 'プロジェクト' },
  { key: 'issues', label: 'チケット' },
  { key: 'issue-detail', label: 'チケット詳細', hidden: true },
  { key: 'settings', label: '設定' },
];

const screenCache = new Map(); // key -> section element

// loadFragment は screens/<key>.html を取得して画面領域に挿入する。
async function loadFragment(key) {
  if (screenCache.has(key)) return screenCache.get(key);
  const host = document.getElementById('screens');
  const section = document.createElement('section');
  section.className = 'screen';
  section.dataset.screen = key;
  try {
    const res = await fetch(`screens/${key}.html`);
    section.innerHTML = res.ok ? await res.text() : '';
  } catch {
    section.innerHTML = '';
  }
  host.appendChild(section);
  screenCache.set(key, section);
  return section;
}

// initScreen は js/screens/<key>.js の init を呼ぶ。未実装ならプレースホルダ。
// import の失敗（モジュール未実装）と init 実行時のエラーを区別する。後者は
// 握りつぶさず、画面にエラー状態を出して通知する（実装済み画面の不具合を隠さない）。
async function initScreen(key, section, params) {
  let mod;
  try {
    mod = await import(`./screens/${key}.js`);
  } catch (e) {
    // モジュール未実装（フェーズ 6 で追加）。プレースホルダを出す。
    if (!section.innerHTML.trim()) {
      section.innerHTML = '<div class="state-empty">この画面は準備中です。</div>';
    }
    return;
  }
  const initFn = mod[`init${toCamel(key)}`] || mod.init;
  if (typeof initFn !== 'function') {
    if (!section.innerHTML.trim()) {
      section.innerHTML = '<div class="state-empty">この画面は準備中です。</div>';
    }
    return;
  }
  try {
    await initFn(section, params);
  } catch (e) {
    section.innerHTML = '<div class="state-error">画面の初期化に失敗しました。</div>';
    toast('画面の読み込みに失敗しました', 'crit');
  }
}

function toCamel(key) {
  return key.split('-').map((p) => p.charAt(0).toUpperCase() + p.slice(1)).join('');
}

async function route() {
  if (!authed) return; // 未認証（ログインオーバーレイ表示中）はルーティングしない
  const raw = (location.hash || '#projects').slice(1);
  const [key, ...rest] = raw.split('/');
  const known = SCREENS.find((s) => s.key === key) || SCREENS[0];

  const section = await loadFragment(known.key);
  for (const el of document.querySelectorAll('.screen')) el.classList.remove('active');
  section.classList.add('active');
  setActiveNav(known.key);
  setTitle(known.label);
  document.getElementById('screens').focus();
  await initScreen(known.key, section, rest);
}

// ---- 認証ゲート ----

async function bootstrap() {
  initTheme();
  let me = null;
  try {
    me = await apiGetJson('/api/auth/me');
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) {
      // 401 以外は想定外。ログイン画面にフォールバックしつつ通知
      toast('起動時の確認に失敗しました', 'crit');
    }
  }

  if (!me) {
    presentLogin();
    return;
  }
  enterApp();
}

let authed = false; // 認証済みか（route のガード）
let wired = false; // 一度だけ行う配線（多重登録防止）

function presentLogin() {
  const node = document.createElement('div');
  showLogin(node);
  initLogin(node, { onSuccess: () => enterApp() });
}

function enterApp() {
  authed = true;
  hideLogin();
  if (!wired) {
    // ドロワー・hashchange・ログアウトの配線は一度だけ（再ログインで多重登録しない）
    initDrawer(SCREENS);
    window.addEventListener('hashchange', route);
    window.rmappLogout = doLogout;
    wired = true;
  }
  if (!location.hash) location.hash = '#projects';
  route();
}

// doLogout はセッションを破棄し、ハッシュを消してログイン画面に戻す。
// hash は history 経由で消して hashchange を発火させない（オーバーレイ下で
// route が走らないように）。
async function doLogout() {
  try { await apiPostJson('/api/auth/logout'); } catch (e) {}
  authed = false;
  history.replaceState(null, '', location.pathname + location.search);
  presentLogin();
}

document.addEventListener('DOMContentLoaded', bootstrap);
