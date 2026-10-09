# CLAUDE.md

Project rules for AI agents. This file is the single source of truth for
machine-readable conventions. Human-facing documentation (`README.md`,
`docs/Design.md`, `docs/Setup.md`, `docs/Manual.md`) is written in Japanese;
**do not read, cite, mirror, or link those files from this document.**

---

## 1. What this project is

A mobile-first HTML5 SPA client for Redmine, plus a Go relay server that is
registered in Redmine as an **OAuth 2.0 application** (authorization code
flow, Redmine 7). Users log in through Redmine's own login/consent screen; the
server holds the resulting OAuth tokens server-side and calls the REST API with
`Authorization: Bearer`. **No Redmine API key (any user's) is ever used, stored
or requested** — and the browser never sees a token.

```
client ──443──► Host Apache ──┬─ /redmine ──► redmine-web  (RedmineDocker stack)
                (TLS, HSTS)   │
                              └─ /        ──► rmapp (this repo: Go relay + SPA)
                                                 │ ▲ OAuth 2.0 authorization code (+PKCE)
                                                 │ │ browser is redirected to /redmine/oauth/authorize
                                                 │ Authorization: Bearer <access token>
                                                 ▼
                                              Redmine 7 REST API (via /redmine)
```

Two sibling repositories define the conventions this project follows:

| Repo | Provides | What we inherit |
|---|---|---|
| `ryu-karura/RedmineDocker` | The Redmine 7.0.2 stack this app talks to | Ops conventions: file-based secrets, shell style, doc structure, Compose-dev / Quadlet-prod split |
| `ryu-karura/IoTDesignTemplate` | SPA + Go server template | Frontend architecture, CSS token system, Go server package layout, config style |

**Redmine itself is out of scope here.** This repo never modifies the
RedmineDocker stack; it only connects to it. Facts we rely on: Redmine 7.0.2
at sub-URI `/redmine` (dev: `http://localhost:8080/redmine/`), PostgreSQL 18 +
PostGIS 3.6, `redmine_gtt` among the 15 baked-in plugins (geometry support
exists from day one), REST API must be enabled in Redmine admin settings
(Redmine's OAuth2 provider — Doorkeeper, `authorization_code` grant only, refresh
tokens on, scopes = Redmine permissions — refuses to authenticate while the
REST API is disabled). **Redmine 7 only**: do not add 5.x/6.x compatibility
code. An admin registers `rmapp` once under Redmine's OAuth application admin
screen (redirect URI `<rmapp public URL>/api/auth/callback`, confidential);
the client secret is shown once and goes to `secrets/` (§4.5).

---

## 2. Repository layout

Mirrors IoTDesignTemplate (`app/` + `server/`), not a React project.

```
.
├── CLAUDE.md
├── README.md
├── docs/                      # Design.md / Setup.md / Manual.md (Japanese); plan.md (implementation plan)
├── app/                       # frontend SPA (vanilla JS, no build step)
│   ├── index.html             # shell only: topbar, nav, <main id="screens">, toast
│   ├── screens/               # per-screen HTML fragments (no <section> wrapper)
│   ├── js/
│   │   ├── app.js             # ES module entry; SCREENS manifest; routing
│   │   ├── common/            # shell.js, api.js, auth.js, tree.js, table.js, utils.js
│   │   ├── screens/           # one <key>.js per screen exporting init<Name>()
│   │   └── vendor/            # vendored libs (Tabulator; later MapLibre). No CDN.
│   └── css/
│       ├── tokens.css         # design tokens — the ONLY file allowed to contain hex
│       ├── base.css
│       ├── layout.css
│       ├── components/        # one file per shared component
│       ├── screens/           # only when a screen needs truly unique styles
│       └── vendor/
├── server/
│   ├── cmd/rmapp/main.go
│   ├── internal/
│   │   ├── config/            # config.yaml load + validation
│   │   ├── auth/              # OAuth login (authorize/callback), sessions, login rate limiting
│   │   ├── credential/        # encrypted OAuth token vault (access + refresh)
│   │   ├── proxy/             # Redmine relay: allowlist + Bearer injection
│   │   ├── redmine/           # typed Redmine REST client + OAuth endpoints (aggregation)
│   │   ├── httpapi/           # handlers, middleware, error envelope
│   │   ├── store/             # SQLite persistence (users, oauth tokens, oauth states, sessions)
│   │   └── webfs/             # static/embedded asset serving
│   ├── config/config.yaml     # comments in Japanese
│   ├── migrations/
│   └── Makefile
├── scripts/                   # generate-secrets.sh, backup.sh, restore.sh, test-stack.sh
├── .claude/
│   ├── rules/                 # frontend.md, server.md, docs.md (path-scoped)
│   └── skills/                # implement, test (+ LESSONS.md), setup, build, docs-sync
└── secrets/                   # git-ignored; created by scripts/generate-secrets.sh
```

---

## 3. Frontend rules (`app/`)

Inherited from IoTDesignTemplate; deviations are called out explicitly.

### 3.1 Architecture

- **Vanilla JS (ES6+ modules), no framework, no bundler, no build step.**
  Never introduce React, Vue, Vite, npm dependencies, or TypeScript.
- **Hash routing.** `js/app.js` holds the `SCREENS` manifest — the single
  source of truth for every screen's `key`, `label`, and `init` function.
  Fragments in `screens/<key>.html` are fetched at runtime into
  `<section data-screen="<key>" class="screen">` under `<main id="screens">`.
- **Deviation from the template:** navigation is mobile-first. The desktop
  sidebar is secondary; on narrow viewports (≤900px) navigation is a slide-in
  drawer (template's `initMobileMenu` pattern), and primary actions sit in
  reach of the thumb. Screen flow is hierarchical
  (projects → issues → issue detail) rather than a flat menu, so the drawer
  lists top-level screens only and back-navigation uses the hash history.
- Modal routes follow the template: `#modal-<key>` entries in the `MODALS`
  array, opened via `js/common/modal.js`.
- Because fragments load via `fetch`, the app must be served over HTTP by the
  Go server — never opened as `file://`, never served by a dumb static server
  (data comes from `/api/...`).

### 3.2 Shared modules — always use, never bypass

| Module | Rule |
|---|---|
| `js/common/api.js` | All HTTP goes through `apiGetJson` / `apiPostJson` / etc. Screens never call `fetch` directly. Every write request (POST/PUT/DELETE) sends `X-Requested-With: XMLHttpRequest` — the server rejects writes without it (CSRF check). |
| `js/common/table.js` | Tabulator wrapper. Hand-rolled `<table>` rendering is forbidden. Project and issue trees use Tabulator's `dataTree` through this wrapper — extend the wrapper, don't call `window.Tabulator` in screens. |
| `js/common/tree.js` | Pure functions turning Redmine's flat `parent.id` arrays into nested tree data for Tabulator. No DOM access in this module; it must be unit-testable in isolation. |
| `js/common/utils.js` | Date/format helpers. Check here before writing a helper in a screen; duplicating logic across screens is forbidden. |

Vendored libraries live under `js/js/vendor/` with their licenses; no CDN
(deploy targets may be offline). Tabulator 6 now; MapLibre GL JS later for the
map feature. Chart.js is NOT carried over — remove it from scope.

### 3.3 Data flow and auth

- On bootstrap `app.js` calls `GET /api/auth/me`; unauthenticated users get
  the login screen, whose only action is a full-page navigation to
  `GET /api/auth/login` (OAuth authorize redirect — never a `fetch`). The
  available screens come from the server response —
  the menu is filtered server-side, never assembled from client-side role
  logic.
- Session state lives in an HttpOnly cookie. **Never store tokens, keys, or
  credentials in localStorage/sessionStorage/IndexedDB.** localStorage is
  allowed only for: theme, tree expand/collapse state, list filters,
  issue-comment drafts (cleared on logout).
- Every list/detail screen implements four explicit states: loading
  (skeleton), empty, error (with retry), populated.

### 3.4 Styling

- Token layering exactly as the template:
  `tokens.css → base.css / layout.css → components/*.css → screens/*.css`.
  **Hex values are legal only in `tokens.css`.** Everything else uses
  `var(--...)`.
- Reuse the template's Ocean Blue token set and names verbatim
  (`--bg`, `--surface`, `--surface-2`, `--fg`, `--muted`, `--border`,
  `--border-strong`, `--primary`, `--on-primary`, `--ok`, `--warn`, `--crit`,
  `--*-soft`, `--space-*`, `--fs-*`, `--radius-*`, shadows). Light/dark via
  the `dark` class on `<html>`, persisted in localStorage key `theme`, with
  the FOUC-prevention inline script in `<head>` (the one permitted inline
  script).
- New tokens added by this project (define in `tokens.css`, both modes):
  `--depth-1`..`--depth-5` (tree-level rail colors) and
  `--status-new` / `--status-open` / `--status-closed` (issue status badges,
  mapped from `--primary` / `--ok` family).
- Semantic colors never carry meaning alone — always pair with an icon or
  text label (template rule; also WCAG).
- Touch targets ≥ 44×44 CSS px. Base styles target 360px viewport; widen
  with `min-width` media queries only.
- Element IDs camelCase; screen keys and CSS classes kebab-case. ISO8601
  timestamps with explicit timezone (`+09:00`).
- Interactive elements get `aria-label`; trees expose
  `role="tree"` / `role="treeitem"`, `aria-expanded`, `aria-level`.

### 3.5 Screens

| key | Screen | Notes |
|---|---|---|
| `login` | Login | single "Log in with Redmine" button → OAuth authorize redirect; error display after a failed callback |
| `projects` | Project list | parent/child tree preserved (Tabulator dataTree) |
| `issues` | Issue list | tree preserved; filter row; closed collapsed by default |
| `issue-detail` | Issue detail | inline field editing; comment composer |
| `settings` | Settings | Redmine authorization status + re-authorize, granted scopes, theme, logout |

Future (do not implement until requested): map rendering of `redmine_gtt`
point/line/polygon geometry with vendored MapLibre GL JS.

---

## 4. Server rules (`server/`)

Follows IoTDesignTemplate's server layout and conventions; deviations noted.

### 4.1 Structure

- `cmd/rmapp/main.go` wires dependencies and starts the server; no business
  logic.
- Package boundaries as in §2. Handlers depend on interfaces; no global
  mutable state; no `init()` side effects.
- **Deviation:** the template pins Go 1.17 for embedded targets. This server
  is **not** deployed to those targets and current dependencies require a
  modern toolchain — this module targets **Go 1.22+**. Do not port the 1.17
  constraint here.
- **Deviation:** the template keeps sessions in memory. This server persists
  users, OAuth tokens, AND sessions in SQLite (`internal/store`) —
  refresh tokens are long-lived by nature and a restart must not log everyone
  out.

### 4.2 HTTP conventions

- JSON everywhere; single error envelope
  `{ "error": { "code": "snake_case_id", "message": "for developers" } }`.
- CSRF: cookie is `SameSite=Lax` and every state-changing endpoint requires
  the `X-Requested-With: XMLHttpRequest` header (template convention, kept
  instead of a token scheme).
- The OAuth login endpoints (`/api/auth/login`, `/api/auth/callback`) are
  rate-limited per client IP (template pattern: lock after 5 consecutive
  failures for 60s). The server never sees a user's Redmine password.
- OAuth hardening, all mandatory: random `state` bound to a short-lived
  server-side record (single use, 10 min) and checked on the callback;
  PKCE `S256` on every authorization request (Redmine does not force it, we
  always send it); the redirect URI is built from config, never from the
  request; the post-login return path is an allowlisted in-app hash, never a
  free URL (no open redirect); session ID is regenerated on login.
- Middleware order: `RequestID → RecoverPanic → AccessLog → Session →
  RequireXHRForWrites → Handler`.
- Session model uses the template's two-axis timeout: idle timeout and
  absolute timeout, both configurable.

### 4.3 Relay / proxy

- Explicit allowlist of `(method, path pattern)` pairs in one declarative
  slice (`internal/proxy/allowlist.go`). Non-matching → 404. Never proxy by
  prefix.
- The relay injects `Authorization: Bearer <access token>` (the session
  user's token). An inbound request carrying `X-Redmine-API-Key` is rejected
  with 400; the relay itself never sends that header. Never forward inbound
  `Authorization`, `Cookie`, or `X-Redmine-Switch-User`.
- Remember the sub-URI: every upstream path is
  `<redmine.base_url>` + `/redmine` + `<api path>` — the sub-URI comes from
  config, never hardcoded in the client or handlers.
- Upstream 5xx surfaces as 502 `upstream_error`. Upstream 401 triggers at most
  one refresh-and-retry; if the refresh fails (`invalid_grant`, revoked, user
  removed the app in Redmine) the stored tokens are marked invalid and the
  response is 409 `redmine_credential_invalid` (SPA re-runs the OAuth login).

### 4.4 Credentials

- **Redmine API keys are forbidden everywhere**: no column, no config key, no
  `X-Redmine-API-Key`, no `/my/account.json` call, no key in test tooling
  (test fixtures obtain OAuth tokens through Redmine, e.g. `rails runner`).
- One OAuth token pair (access + refresh) per user, holding the scopes the
  user consented to. Access tokens are short-lived (Redmine default 2 h);
  refresh rotates the refresh token, so refreshes are serialized per user
  (single-flight) and the new pair is persisted before use — a lost rotation
  kills the grant.
- Tokens encrypted AES-256-GCM; KEK from config (value or file path), never
  logged. The token-holding types' `MarshalJSON` returns `"[redacted]"`.
- Identity comes from Redmine: after the code exchange, `GET /users/current.json`
  with the new token defines the user row (`redmine_user_id` is the key;
  login/name are refreshed on each login). There is no local password and no
  passkey.

### 4.5 Config

- Single `server/config/config.yaml`, comments in Japanese, loaded and
  validated once at startup; a missing required key is a fatal error naming
  the key. Style follows the template (`listen`, `webroot`, `baseURL`,
  `serveStatic`, `noCache`, `session.*`, `logLevel`) extended with
  `crypto.*`, `redmine.*` (incl. `redmine.oauth.*`), `database.*`, `features.*`.
- Precedence: flag > env (`RMAPP_` prefix) > file > default.
- Secrets follow RedmineDocker's convention: **file-based, never committed,
  never plain env vars.** `scripts/generate-secrets.sh` writes
  `secrets/session_key.txt` and `secrets/kek.txt` (mode 600, git-ignored);
  the OAuth client secret (issued by Redmine, shown once) lives in
  `secrets/redmine_oauth_client_secret.txt`, which the script does not
  generate — it creates an empty placeholder and `config` fails fast naming
  the key if it is empty. Config references secrets by path (`*_file` keys).
- `webauthn.*` and `features.passwordBootstrap` no longer exist; OAuth keys
  live under `redmine.oauth.*` (client id, secret file, redirect URI, scopes,
  and `redmine.publicBaseURL` for the browser-facing authorize URL when it
  differs from the server-to-server `redmine.baseURL`).

### 4.6 Logging and errors

- `log/slog`, structured. Never log bodies, cookies, session IDs, OAuth
  tokens / authorization codes / `state` / PKCE verifiers, or the client secret.
- Wrap errors with `%w`; never discard with `_`. `panic` only in `main`
  startup wiring.
- User-visible Japanese error strings live in error values near their package
  (template pattern), keyed to envelope codes.

### 4.7 Tests

- Table-driven handler tests: success / unauthenticated / malformed /
  upstream failure. Redmine client tested against `httptest.Server` only.
- `internal/httpapi` tests are the API suite; everything else is the unit
  suite (template's split). Makefile targets: `test-unit`, `test-api`.

---

## 5. Shell scripts (`scripts/`)

RedmineDocker's conventions apply verbatim:

- bash, `set -euo pipefail`, Japanese header comment block (purpose, usage,
  prerequisites), Japanese user-facing output.
- `shellcheck scripts/*.sh` before committing shell changes.
- `log()`/`die()` helpers with timestamps; destructive actions require a
  typed confirmation literal (e.g. `RESTORE`).
- Idempotent where possible; never require a specific working directory.
- `test-stack.sh` is the integration test: boots the server against a running
  RedmineDocker dev stack and checks login page, health endpoints, the OAuth
  authorize redirect (state + PKCE present), and one allowlisted proxy
  round-trip using an OAuth token provisioned via Redmine (never an API key).

---

## 6. Documentation ownership

| File | Owns |
|---|---|
| `README.md` | overview, quick start, links |
| `docs/Design.md` | architecture, data model, API, screens, config catalog |
| `docs/Setup.md` | build/deploy procedures, config values |
| `docs/Manual.md` | operations and end-user procedures |
| `docs/plan.md` | staged implementation plan + progress checklist (maintained per the `implement` skill) |
| `CLAUDE.md` | machine-facing conventions only |
| `.claude/rules/*.md` | path-scoped detail rules (frontend/server/docs), template-style |
| `.claude/skills/test/LESSONS.md` | prevention rules distilled from test failures (maintained per the `test` skill) |

No duplication across files; cross-reference instead. When behaviour changes,
update the affected docs in the same commit (both reference repos enforce
this; so do we). `.github/copilot-instructions.md`, if added, is a pointer to
this file, never a second source of truth.

---

## 7. Skills

`.claude/skills/` for this repo (patterned after both reference repos).
`implement` and `test` exist; the rest are planned:

| Skill | Purpose |
|---|---|
| `implement` | **mandatory before any implementation work**: plan-driven staged workflow against `docs/plan.md` — one phase at a time, checkbox per commit; unattended (scheduled-trigger) runs additionally follow its "Unattended runs" rules (PR discovery → continue-or-recreate, safety, run log, stall detection) |
| `test` | which of test-unit / test-api / node --test / test-e2e / test-stack to run per change (CI `.github/workflows/ci.yml` re-runs the suites on every push as independent verification); failure → prevention-rule loop into `LESSONS.md` |
| `setup` | boot RedmineDocker dev stack + this server for local work |
| `build` | build server, run shellcheck, verify static assets |
| `docs-sync` | change-type → document map; keeps §6 honest |
| `frontend-rules` | pointer into `.claude/rules/frontend.md` for screen work |

Plus the generally available skills: `superpowers-dev:*` (brainstorming,
writing-plans, TDD, systematic-debugging, verification-before-completion,
code review pair), `code-review` (the harness-provided diff reviewer used
as the phase quality gate — see the `implement` skill), `frontend-design`,
`design:accessibility-review`, `design:ux-copy`,
`elements-of-style:writing-clearly-and-concisely`.

---

## 8. Git workflow

- Conventional Commits, subject ≤ 50 chars; scopes: `app`, `server`,
  `scripts`, `docs`.
- One logical change per commit; don't mix formatting with behaviour.
- Work on the assigned feature branch; do not open a pull request unless
  explicitly asked (RedmineDocker convention).

---

## 9. Non-negotiables

1. No Redmine API key is ever used, stored, requested or logged — and no
   OAuth token ever reaches the browser. Login is Redmine's OAuth 2.0 only.
2. No proxy path outside the allowlist.
3. No secret in logs, errors, responses, or committed files — secrets are
   files under `secrets/`, generated by script.
4. No frameworks, bundlers, or CDNs in `app/`; hex only in `tokens.css`.
5. No implementation without a failing test first.
6. This repo never modifies the RedmineDocker stack.
7. No implementation outside the current phase of `docs/plan.md`; progress
   is checked off in the same commit (`implement` skill).
8. Unexpected test failures leave a prevention rule in
   `.claude/skills/test/LESSONS.md` (`test` skill); LESSONS.md is read
   before every task.
