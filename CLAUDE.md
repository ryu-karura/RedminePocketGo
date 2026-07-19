# CLAUDE.md

Project rules for AI agents. This file is the single source of truth for
machine-readable conventions. Human-facing documentation lives in
`README.md` and `docs/` and is written in Japanese; **do not read, cite,
mirror, or link those files from this document.**

---

## 1. Project identity

A mobile-first HTML5 SPA that talks to an existing Redmine instance
through a Go relay server. The relay owns authentication (WebAuthn /
passkey) and holds Redmine API keys server-side. The browser never sees
a Redmine API key.

```
[SPA: HTML5 / TypeScript]
        | same-origin, HttpOnly session cookie
[Go relay: auth + API proxy]
        | X-Redmine-API-Key
[Redmine + PostgreSQL (Docker)]
```

---

## 2. Repository layout

```
.
├── CLAUDE.md
├── README.md
├── docs/
│   ├── DESIGN.md
│   ├── SETUP.md
│   └── MANUAL.md
├── backend/
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── config/          # config load + validation
│   │   ├── auth/            # WebAuthn ceremonies
│   │   ├── session/         # cookie session store
│   │   ├── credential/      # API key vault (encrypted)
│   │   ├── proxy/           # Redmine relay + allowlist
│   │   ├── redmine/         # typed Redmine client
│   │   ├── httpx/           # middleware, error envelope
│   │   └── store/           # SQLite/Postgres repositories
│   ├── migrations/
│   └── configs/config.example.yaml
├── frontend/
│   ├── src/
│   │   ├── app/             # bootstrap, router, providers
│   │   ├── pages/           # one folder per screen
│   │   ├── components/      # shared presentational components
│   │   ├── features/        # domain logic per feature
│   │   ├── api/             # generated/hand-written API client
│   │   ├── stores/          # state management
│   │   ├── styles/          # design tokens, theme
│   │   └── types/
│   ├── public/
│   └── index.html
├── deploy/
│   ├── docker-compose.yml
│   └── redmine/             # Redmine container assets
└── scripts/                 # operational shell scripts
```

Do not invent new top-level directories without an explicit request.

---

## 3. Language and toolchain

| Area | Choice | Notes |
|---|---|---|
| Backend | Go 1.22+ | stdlib `net/http` + `chi` router only |
| WebAuthn | `github.com/go-webauthn/webauthn` | no alternatives |
| Backend DB | SQLite (default), PostgreSQL (optional) | accessed via `internal/store` only |
| Frontend | TypeScript 5.x, Vite, React 18 | no CRA, no Next.js |
| Styling | CSS Modules + CSS custom properties | no Tailwind, no CSS-in-JS runtime |
| Map (future) | MapLibre GL JS | not Google Maps, not Leaflet |
| Test (Go) | stdlib `testing` + `testify/require` | |
| Test (TS) | Vitest + Testing Library | |

Never add a dependency that duplicates something already listed.

---

## 4. Backend rules

### 4.1 Structure

- `cmd/server/main.go` only wires dependencies and starts the server. No
  business logic.
- Every `internal/` package exposes an interface at its boundary.
  Handlers depend on interfaces, never on concrete structs from another
  package.
- No global mutable state. No `init()` side effects. Configuration is
  passed explicitly from `main`.

### 4.2 HTTP

- All responses are JSON. Errors use one envelope:

  ```json
  { "error": { "code": "invalid_credential", "message": "..." } }
  ```

  `code` is a stable snake_case identifier; `message` is for logs and
  developers, not for direct display.
- Every handler must accept `context.Context` from the request and
  propagate it to all downstream calls.
- Middleware order is fixed: `RequestID → RecoverPanic → AccessLog →
  CORS(noop for same-origin) → Session → CSRF → Handler`.

### 4.3 Relay / proxy

- The Redmine proxy uses an **explicit allowlist** of
  `(method, path pattern)` pairs. A request that does not match returns
  `404`. Never proxy by prefix alone.
- The allowlist lives in one file, `internal/proxy/allowlist.go`, as a
  single declarative slice.
- The relay injects `X-Redmine-API-Key` in the proxy director. Any
  inbound request carrying that header is rejected with `400` before it
  reaches the director.
- Never forward inbound `Authorization`, `Cookie`, or
  `X-Redmine-Switch-User` headers to Redmine.
- Redmine 5xx must be surfaced as `502` with `code: "upstream_error"`,
  never as a naked passthrough.

### 4.4 Credentials

- Redmine API keys are stored encrypted with AES-256-GCM. The key
  encryption key comes from configuration and is never written to logs,
  errors, or responses.
- A stored credential is keyed by `(user_id)`, not by device. Devices
  (passkeys) are separate rows linked to the same user. Any passkey
  belonging to a user unlocks that user's single Redmine API key.
- A credential value must never appear in any struct that is JSON-
  serialised toward the client. Enforce with a dedicated type whose
  `MarshalJSON` returns `"[redacted]"`.

### 4.5 Errors and logging

- Wrap errors with `fmt.Errorf("...: %w", err)`. Never discard an error
  with `_`.
- Use `log/slog` with structured fields. Never log request bodies,
  cookies, API keys, or WebAuthn challenge material.
- `panic` is only permitted in `main` during startup wiring.

### 4.6 Tests

- Every handler has a table-driven test covering: success, unauthorised,
  malformed input, and upstream failure.
- The Redmine client is tested against `httptest.Server`, never against a
  live instance.

---

## 5. Frontend rules

### 5.1 Structure

- One folder per page under `src/pages/`, containing the page component,
  its styles, and page-local components only.
- Anything used by two or more pages moves to `src/components/`.
- API access happens only in `src/api/`. Components never call `fetch`
  directly.
- State: server state via TanStack Query; UI state via React state or
  Zustand. Never duplicate server data into a global store.

### 5.2 Rendering and data

- All requests are same-origin and rely on the session cookie. Never
  store tokens or keys in `localStorage`, `sessionStorage`, or
  `IndexedDB`.
- Every list view must handle four states explicitly: loading, empty,
  error, and populated. A missing empty state is a defect.
- Tree structures (projects, issues) are rendered from a flat array
  transformed into a tree in a pure function under `src/features/`. Do
  not build trees inside components.
- Lists must be virtualised when they may exceed 200 rows.

### 5.3 Styling

- Colours, spacing, radii, and typography come exclusively from CSS
  custom properties defined in `src/styles/tokens.css`. No literal hex
  values, no magic pixel numbers in component styles.
- Mobile-first: base styles target a 360px viewport; widen with
  `min-width` media queries only.
- Touch targets are at least 44×44 CSS pixels.
- Colour is never the only carrier of meaning; pair it with an icon or
  text label.
- Contrast must meet WCAG 2.1 AA (4.5:1 for body text).

### 5.4 Accessibility

- Every interactive element is reachable and operable by keyboard.
- Tree views use `role="tree"` / `role="treeitem"` with `aria-expanded`
  and `aria-level`.
- Focus is moved deliberately after navigation and after modal open and
  close.

### 5.5 Prohibited

- No `dangerouslySetInnerHTML` without sanitisation through a single
  shared helper.
- No inline `<script>` in `index.html`.
- No `any` in TypeScript. Use `unknown` and narrow.

---

## 6. Screens

| Route | Screen | Notes |
|---|---|---|
| `/login` | Login | passkey primary, password fallback for bootstrap |
| `/projects` | Project list | parent/child tree preserved |
| `/projects/:id/issues` | Issue list | parent/child tree preserved |
| `/issues/:id` | Issue detail | |
| `/settings` | Settings | passkey management, API key linkage |

Future (do not implement until requested): map rendering of point, line,
and polygon geometry for projects and issues, sourced from the
`redmine_gtt` plugin.

---

## 7. Configuration

- All configuration is loaded once at startup by `internal/config` and
  validated there. A missing or invalid required value is a fatal
  startup error with a message naming the offending key.
- Precedence: command-line flag > environment variable > config file >
  built-in default.
- Environment variables are prefixed `RMAPP_`.
- Secrets are accepted from environment variables or a file path, never
  from a committed config file.
- `configs/config.example.yaml` must stay in sync with the config struct.
  Its comments are written in Japanese.

---

## 8. Shell scripts

- Every script under `scripts/` starts with `#!/usr/bin/env bash` and
  `set -euo pipefail`.
- Comments and user-facing `echo` output are written in Japanese.
- Each script begins with a Japanese header comment stating its purpose,
  usage, and prerequisites.
- Scripts are idempotent where possible and must not require being run
  from a specific working directory.

---

## 9. Commits and changes

- Conventional Commits. Subject line ≤ 50 characters.
- Scopes: `backend`, `frontend`, `deploy`, `docs`, `scripts`.
- One logical change per commit. Do not mix formatting with behaviour.
- When behaviour changes, update the affected documentation in the same
  commit.

---

## 10. Documentation ownership

Each human-facing document owns a distinct scope. Do not duplicate
content across them; cross-reference instead.

| File | Scope |
|---|---|
| `README.md` | overview, quick start, links |
| `docs/DESIGN.md` | architecture, data model, API, screens, config items |
| `docs/SETUP.md` | build and deploy procedures, config values |
| `docs/MANUAL.md` | day-to-day operation and end-user procedures |

This file (`CLAUDE.md`) owns machine-facing conventions only and must
not restate architecture decisions in detail — reference the concepts,
not the prose.

---

## 11. Related skills

Skills expected to be available to agents working on this repository:

| Skill | Use for |
|---|---|
| `superpowers-dev:brainstorming` | before any new feature |
| `superpowers-dev:writing-plans` | multi-step work |
| `superpowers-dev:test-driven-development` | all implementation |
| `superpowers-dev:systematic-debugging` | any bug or test failure |
| `superpowers-dev:verification-before-completion` | before claiming done |
| `superpowers-dev:requesting-code-review` | before merge |
| `superpowers-dev:receiving-code-review` | acting on feedback |
| `superpowers-dev:using-git-worktrees` | isolated feature work |
| `frontend-design` | screen and visual design work |
| `design:accessibility-review` | before shipping any screen |
| `design:design-system` | token and component consistency |
| `design:ux-copy` | button, error, and empty-state wording |
| `elements-of-style:writing-clearly-and-concisely` | any prose |

---

## 12. Non-negotiables

1. No Redmine API key ever reaches the browser.
2. No proxy path outside the allowlist.
3. No secret in logs, errors, or responses.
4. No new dependency that duplicates an existing one.
5. No implementation without a failing test first.
