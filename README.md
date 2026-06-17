# 🍬 Candyland

A solo **agent-orchestration dashboard**. You describe a goal; Candyland guides
you through it (developer or non-developer mode), drives a run with headless
Claude Code, and shows every agent's live state and output — so you review one
finished PR instead of juggling sessions by hand.

A single standalone binary: a Go [ooo](https://github.com/benitogf/ooo) realtime
backend that embeds and serves the built React UI, built on the
[mono](https://github.com/benitogf/mono) boilerplate.

## Architecture

```
React UI  ──ooo-client (WebSocket)──▶  ooo realtime state  ◀── conductor
  │  guided wizard → planning Q&A                                │ spawns
  │  live run workspace (agents/board/tasks/sessions)            ▼
  └──REST (/api/runs, …)────────────────────────────▶  executor
                                                  ├─ claude: real headless
                                                  │    `claude -p --output-format
                                                  │    stream-json` → parsed into
                                                  │    ooo state
                                                  └─ scripted: deterministic
                                                       timeline (fallback / demo)
```

- The **conductor** (`internal/conductor`) creates runs and drives them with an
  **executor**, publishing every state change to ooo (`runs/<id>`). The UI
  subscribes — there is **no client-side mock**; the backend is the single source
  of truth.
- The **claude executor** spawns a real headless `claude -p … --output-format
  stream-json` process and maps its events into live run state. Used whenever the
  `claude` CLI is on `PATH`.
- The **scripted executor** drives a deterministic timeline when claude isn't
  available, so the UI always shows genuine, moving ooo state.
- Override with `CANDYLAND_EXECUTOR=claude|scripted`.

## Run

```bash
npm install
npm run build          # vite → ./build (embedded by the Go binary)
go run .               # UI on http://localhost:8080, realtime+API on :8888
```

Dev (UI hot-reload against the backend):

```bash
go run . &             # backend on :8888
npm run dev            # Vite dev server on :3000 → ooo-client talks to :8888
```

## Verify

```bash
npm run validate          # mermaid diagrams parse
npm run validate:layout   # no horizontal scroll / contained overflow (Playwright)
npm run validate:flows    # planning, autocomplete, progress, terminal (Playwright)
go build ./...            # backend compiles
node scripts/e2e.mjs      # full stack: real binary + live ooo flow (Playwright)
```

## Releases

Pushing a `v*` tag builds standalone single-binary releases (backend + embedded
UI) for linux/darwin/windows (amd64 + arm64) via
[`.github/workflows/release.yml`](.github/workflows/release.yml).

## Stack

| Layer    | Choice                                                          |
| -------- | --------------------------------------------------------------- |
| UI       | React 19, Vite 7, MUI 7, xterm                                  |
| Realtime | `ooo` + `ooo-client` over WebSocket                             |
| Backend  | Go (ooo server + conductor), `mono` boilerplate, single binary |
| Agents   | headless `claude -p --output-format stream-json`                |

Open **How it works** in the app for the full design.
