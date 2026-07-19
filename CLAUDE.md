# CLAUDE.md

Project rules for AI agents. This file is the single source of truth for
machine-readable conventions. Human-facing documentation (`README.md`,
`docs/Design.md`, `docs/Setup.md`, `docs/Manual.md`) is written in Japanese;
**do not read, cite, mirror, or link those files from this document.**

---

## 1. What this project is

A mobile-first HTML5 SPA client for Redmine, plus a Go relay server that owns
authentication (WebAuthn / passkey) and holds Redmine API keys server-side.
The browser never sees a Redmine API key.

```
client ──443──► Host Apache ──┬─ /redmine ──► redmine-web  (RedmineDocker stack)
                (TLS, HSTS)   │
                              └─ /        ──► rmapp (this repo: Go relay + SPA)
                                                 │ X-Redmine-API-Key
                                                 ▼
                                              Redmine REST API (via /redmine)
```

Two sibling repositories define the conventions this project follows:

| Repo | Provides | What we inherit |
|---|---|---|
| `ryu-karura/RedmineDocker` | The Redmine 6.1.3 stack this app talks to | Ops conventions: file-based secrets, shell style, doc structure, Compose-dev / Quadlet-prod split |
| `ryu-karura/IoTDesignTemplate` | SPA + Go server template | Frontend architecture, CSS token system, Go server package layout, config style |

**Redmine itself is out of scope here.** This repo never modifies the
RedmineDocker stack; it only connects to it. Facts we rely on: Redmine 6.1.3
at sub-URI `/redmine` (dev: `http://localhost:8080/redmine/`), PostgreSQL 18 +
PostGIS 3.6, `redmine_gtt` among the 13 baked-in plugins (geometry support
exists from day one), REST API must be enabled in Redmine admin settings.

---

## 2. Repository layout

Mirrors IoTDesignTemplate (`app/` + `server/`), not a React project.

```
.
├── CLAUDE.md
├── README.md
├── docs/                      # Design.md / Setup.md / Manual.md (Japanese)
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
│   │   ├── auth/              # WebAuthn ceremonies, sessions, login rate limiting
│   │   ├── credential/        # encrypted Redmine API key vault
│   │   ├── proxy/             # Redmine relay: allowlist + header injection
│   │   ├── redmine/           # typed Redmine REST client (bootstrap, aggregation)
│   │   ├── httpapi/           # handlers, middleware, error envelope
│   │   ├── store/             # SQLite persistence (users, passkeys, sessions)
│   │   └── webfs/             # static/embedded asset serving
│   ├── config/config.yaml     # comments in Japanese
│   ├── migrations/
│   └── Makefile
├── scripts/                   # generate-secrets.sh, backup.sh, restore.sh, test-stack.sh
├── .claude/
│   ├── rules/                 # frontend.md, server.md, docs.md (path-scoped)
│   └── skills/                # setup, build, test, docs-sync
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
| `js/common/auth.js` | WebAuthn ceremony helpers: base64url ⇄ ArrayBuffer conversion, `navigator.credentials` calls, feature detection. Screens never touch `navigator.credentials` directly. |
| `js/common/table.js` | Tabulator wrapper. Hand-rolled `<table>` rendering is forbidden. Project and issue trees use Tabulator's `dataTree` through this wrapper — extend the wrapper, don't call `window.Tabulator` in screens. |
| `js/common/tree.js` | Pure functions turning Redmine's flat `parent.id` arrays into nested tree data for Tabulator. No DOM access in this module; it must be unit-testable in isolation. |
| `js/common/utils.js` | Date/format helpers. Check here before writing a helper in a screen; duplicating logic across screens is forbidden. |

Vendored libraries live under `js/js/vendor/` with their licenses; no CDN
(deploy targets may be offline). Tabulator 6 now; MapLibre GL JS later for the
map feature. Chart.js is NOT carried over — remove it from scope.

### 3.3 Data flow and auth

- On bootstrap `app.js` calls `GET /api/auth/me`; unauthenticated users get
  the login screen. The available screens come from the server response —
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
| `login` | Login | passkey primary; enrollment-code path; Redmine-password bootstrap |
| `projects` | Project list | parent/child tree preserved (Tabulator dataTree) |
| `issues` | Issue list | tree preserved; filter row; closed collapsed by default |
| `issue-detail` | Issue detail | inline field editing; comment composer |
| `settings` | Settings | passkey/device management, Redmine link status, theme |

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
  is **not** deployed to those targets and `go-webauthn/webauthn` requires a
  modern toolchain — this module targets **Go 1.22+**. Do not port the 1.17
  constraint here.
- **Deviation:** the template keeps sessions in memory. This server persists
  users, passkey credentials, AND sessions in SQLite (`internal/store`) —
  passkeys are long-lived by nature and a restart must not log everyone out.

### 4.2 HTTP conventions

- JSON everywhere; single error envelope
  `{ "error": { "code": "snake_case_id", "message": "for developers" } }`.
- CSRF: cookie is `SameSite=Lax` and every state-changing endpoint requires
  the `X-Requested-With: XMLHttpRequest` header (template convention, kept
  instead of a token scheme).
- Login and passkey ceremonies are rate-limited (template pattern: lock after
  5 consecutive failures for 60s; constant-time behavior for unknown users —
  keep the dummy-hash trick for the password-bootstrap path).
- Middleware order: `RequestID → RecoverPanic → AccessLog → Session →
  RequireXHRForWrites → Handler`.
- Session model uses the template's two-axis timeout: idle timeout and
  absolute timeout, both configurable.

### 4.3 Relay / proxy

- Explicit allowlist of `(method, path pattern)` pairs in one declarative
  slice (`internal/proxy/allowlist.go`). Non-matching → 404. Never proxy by
  prefix.
- The relay injects `X-Redmine-API-Key`; an inbound request carrying that
  header is rejected with 400. Never forward inbound `Authorization`,
  `Cookie`, or `X-Redmine-Switch-User`.
- Remember the sub-URI: every upstream path is
  `<redmine.base_url>` + `/redmine` + `<api path>` — the sub-URI comes from
  config, never hardcoded in the client or handlers.
- Upstream 5xx surfaces as 502 `upstream_error`; upstream 401 marks the
  stored credential invalid and returns 409 `redmine_credential_invalid`.

### 4.4 Credentials

- One Redmine API key per user (Redmine itself has exactly one per account);
  passkeys are per-device rows linked to the user. Never model per-device API
  keys.
- API keys encrypted AES-256-GCM; KEK from config (value or file path),
  never logged. The key-holding type's `MarshalJSON` returns `"[redacted]"`.

### 4.5 Config

- Single `server/config/config.yaml`, comments in Japanese, loaded and
  validated once at startup; a missing required key is a fatal error naming
  the key. Style follows the template (`listen`, `webroot`, `baseURL`,
  `serveStatic`, `noCache`, `session.*`, `logLevel`) extended with
  `webauthn.*`, `crypto.*`, `redmine.*`, `database.*`, `features.*`.
- Precedence: flag > env (`RMAPP_` prefix) > file > default.
- Secrets follow RedmineDocker's convention: **file-based, never committed,
  never plain env vars.** `scripts/generate-secrets.sh` writes
  `secrets/session_key.txt` and `secrets/kek.txt` (mode 600, git-ignored);
  config references them by path (`*_file` keys).

### 4.6 Logging and errors

- `log/slog`, structured. Never log bodies, cookies, session IDs, API keys,
  or WebAuthn challenge material.
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
  RedmineDocker dev stack and checks login page, health endpoints, and one
  allowlisted proxy round-trip.

---

## 6. Documentation ownership

| File | Owns |
|---|---|
| `README.md` | overview, quick start, links |
| `docs/Design.md` | architecture, data model, API, screens, config catalog |
| `docs/Setup.md` | build/deploy procedures, config values |
| `docs/Manual.md` | operations and end-user procedures |
| `CLAUDE.md` | machine-facing conventions only |
| `.claude/rules/*.md` | path-scoped detail rules (frontend/server/docs), template-style |

No duplication across files; cross-reference instead. When behaviour changes,
update the affected docs in the same commit (both reference repos enforce
this; so do we). `.github/copilot-instructions.md`, if added, is a pointer to
this file, never a second source of truth.

---

## 7. Skills

`.claude/skills/` planned for this repo (patterned after both reference
repos):

| Skill | Purpose |
|---|---|
| `setup` | boot RedmineDocker dev stack + this server for local work |
| `build` | build server, run shellcheck, verify static assets |
| `test` | which of test-unit / test-api / test-stack to run per change |
| `docs-sync` | change-type → document map; keeps §6 honest |
| `frontend-rules` | pointer into `.claude/rules/frontend.md` for screen work |

Plus the generally available skills: `superpowers-dev:*` (brainstorming,
writing-plans, TDD, systematic-debugging, verification-before-completion,
code review pair), `frontend-design`, `design:accessibility-review`,
`design:ux-copy`, `elements-of-style:writing-clearly-and-concisely`.

---

## 8. Git workflow

- Conventional Commits, subject ≤ 50 chars; scopes: `app`, `server`,
  `scripts`, `docs`.
- One logical change per commit; don't mix formatting with behaviour.
- Work on the assigned feature branch; do not open a pull request unless
  explicitly asked (RedmineDocker convention).

---

## 9. Non-negotiables

1. No Redmine API key ever reaches the browser.
2. No proxy path outside the allowlist.
3. No secret in logs, errors, responses, or committed files — secrets are
   files under `secrets/`, generated by script.
4. No frameworks, bundlers, or CDNs in `app/`; hex only in `tokens.css`.
5. No implementation without a failing test first.
6. This repo never modifies the RedmineDocker stack.
