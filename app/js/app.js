// app.js — ES モジュールのエントリ。SCREENS マニフェスト（全画面の唯一の
// 一覧）、ハッシュルーティング、起動時の GET /api/auth/me と認証ゲート。

import { apiGetJson, apiPostJson, ApiError } from './common/api.js';
import {
  initTheme, initDrawer, setActiveNav, setTitle, toast, showLogin, hideLogin,
} from './common/shell.js';
import { openModal, closeModal, isModalHash } from './common/modal.js';
import { initLogin } from './screens/login.js';

// SCREENS: key / label / init。login はオーバーレイなのでナビには出さない。
// 業務画面（projects 以降）はフェーズ 6 で js/screens/<key>.js を実装する。
export const SCREENS = [
  { key: 'projects', label: 'プロジェクト' },
  { key: 'issues', label: 'チケット' },
  { key: 'issue-detail', label: 'チケット詳細', hidden: true },
  { key: 'settings', label: '設定' },
];

// MODALS: #modal-<key> ルートの一覧（Design.md §3.1・§7.1）。画面と異なり
// フラグメントは毎回取り直す（状態を使い回さない使い切りの UI のため）。
export const MODALS = [
  { key: 'issue-create' },
];

const screenCache = new Map(); // key -> Promise<section element>

// loadFragment は screens/<key>.html を取得して画面領域に挿入する。
// 取得は非同期なので「解決済み要素」ではなく Promise をキャッシュする。
// これにより route が並行して 2 回呼ばれても、フラグメントは 1 枚しか作られない
//（未解決の間に両者が has()=false を見て二重生成する競合を防ぐ）。
function loadFragment(key) {
  if (screenCache.has(key)) return screenCache.get(key);
  const p = (async () => {
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
    return section;
  })();
  screenCache.set(key, p);
  return p;
}

// openModalRoute は #modal-<key>/<params> を開く。フラグメントは screens/ と
// 違い使い切りなのでキャッシュしない。裏の画面はそのまま（route() 側で背景の
// 再描画はしない）。
async function openModalRoute(key, params) {
  const known = MODALS.find((m) => m.key === key);
  if (!known) return;
  const container = document.createElement('div');
  try {
    const res = await fetch(`screens/modal-${key}.html`);
    container.innerHTML = res.ok ? await res.text() : '';
  } catch {
    container.innerHTML = '';
  }
  let mod;
  try {
    mod = await import(`./screens/modal-${key}.js`);
  } catch (e) {
    return;
  }
  const initFn = mod[`initModal${toCamel(key)}`];
  openModal(container, {
    onClose: () => {
      // Esc・背景クリック・キャンセルボタンいずれの場合も、ハッシュが
      // モーダルのままなら元の画面へ戻す（ハッシュと表示状態を一致させる）。
      if (isModalHash(location.hash)) history.back();
    },
  });
  if (typeof initFn === 'function') await initFn(container, params);
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
  if (isModalHash(location.hash)) {
    const [modalKey, ...modalParams] = location.hash.slice('#modal-'.length).split('/');
    await openModalRoute(modalKey, modalParams);
    return; // 背後の画面はそのまま（モーダルは前面に重ねるだけ）
  }
  closeModal(); // 通常画面へ遷移するときは開いていたモーダルを必ず閉じる
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
  // hash を設定すると hashchange リスナーが route を呼ぶ。二重呼び出し
  //（＝画面フラグメントの二重生成）を避けるため、設定した場合は直接 route
  // せずリスナーに任せる。既に hash があるなら自分で route する。
  if (!location.hash) location.hash = '#projects';
  else route();
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
