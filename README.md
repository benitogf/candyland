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

## Platforms

Runs on **Linux, macOS, WSL, and Windows** — a single self-contained binary per
OS/arch. The app detects the platform at runtime and reports it (plus dependency
status and install commands) in **Setup** (the status chip in the top bar).

Dependencies:
- **Claude Code** (`claude`) — required for *real* runs. Without it the app still
  works in **simulated** mode (a faithful demo); the UI says so and offers the
  install command for your platform. Install: `curl -fsSL https://claude.ai/install.sh | bash`
  (Linux/macOS/WSL) or `irm https://claude.ai/install.ps1 | iex` (Windows).
- **git** — for worktrees and opening PRs.

If the server isn't running, the UI shows a clear banner with start instructions
rather than failing silently; REST/connection errors surface as toasts.

## Install (released binary)

```bash
# Linux / macOS / WSL
curl -fsSL https://raw.githubusercontent.com/benitogf/candyland/main/install.sh | sh
# Windows (PowerShell)
irm https://raw.githubusercontent.com/benitogf/candyland/main/install.ps1 | iex
```

## Run (from source)

```bash
npm install
npm run build          # vite → ./build (embedded by the Go binary)
go run .               # UI on http://localhost:8080, realtime+API on :8888
```

Force a mode with `CANDYLAND_EXECUTOR=claude|scripted`.

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
go test ./...             # backend unit tests (conductor resilience, httpapi)
node scripts/e2e.mjs      # full stack: real binary + live ooo flow (Playwright)
node scripts/check-system.mjs      # real binary + /api/system platform/deps check
node scripts/check-workspaces.mjs  # real binary + /api/workspaces CRUD check
```

## Releases

Same flow as detritus — a manual bump/tag/release you trigger from a prompt
(just ask Claude to "release candyland X.Y.Z") or directly:

```bash
scripts/release.sh 0.1.0   # from main, clean tree → tags v0.1.0 and pushes
```

The `v*` tag triggers the workflow, which builds the standalone single binaries
(backend + embedded UI, version injected via `-ldflags`) for
linux/darwin/windows (amd64 + arm64) and publishes the GitHub Release that
`install.sh` / `install.ps1` pull from. The workflow currently lives at
[`ci/release.yml`](ci/release.yml) — activate it once per
[`ci/README.md`](ci/README.md) (the CLI token that opened these PRs lacks the
`workflow` scope to push under `.github/workflows/`).

## Stack

| Layer    | Choice                                                          |
| -------- | --------------------------------------------------------------- |
| UI       | React 19, Vite 7, MUI 7, xterm                                  |
| Realtime | `ooo` + `ooo-client` over WebSocket                             |
| Backend  | Go (ooo server + conductor), `mono` boilerplate, single binary |
| Agents   | headless `claude -p --output-format stream-json`                |

Open **How it works** in the app for the full design.
