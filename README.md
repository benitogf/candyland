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
  └──REST (/api/runs, …)────────────────────────────▶  claude executor
                                                       real headless
                                                       `claude -p --output-format
                                                       stream-json`, in git
                                                       worktrees → integrate →
                                                       push → open one PR
```

- The **conductor** (`internal/conductor`) creates runs and drives them with the
  **claude executor**, publishing every state change to ooo (`runs/<id>`). The UI
  subscribes — there is **no mock and no demo mode**; the backend is the single
  source of truth and a run only ever reflects real agent work.
- The **claude executor** resolves the run's workspace to its repo, creates a run
  branch, spawns real headless `claude -p … --output-format stream-json` processes
  (a tech lead partitions the work; one coder per fork-safe task runs in its own
  git worktree), maps their events into live run state, integrates the worktrees,
  then commits, pushes, and opens a single pull request.
- If `claude` isn't installed or authenticated, a run **fails honestly** with an
  actionable error — there is no scripted/simulated fallback.

## Platforms

Runs on **Linux, macOS, WSL, and Windows** — a single self-contained binary per
OS/arch. The app detects the platform at runtime and reports it (plus dependency
status and install commands) in **Setup** (the status chip in the top bar).

Dependencies:
- **Claude Code** (`claude`) — required to run; it drives the agents. Without it a
  run can't start (there is no demo mode) and the UI says so with the install
  command for your platform. Install: `curl -fsSL https://claude.ai/install.sh | bash`
  (Linux/macOS/WSL) or `irm https://claude.ai/install.ps1 | iex` (Windows).
- **git** — for the run branch and worktrees.
- **GitHub CLI** (`gh`) — opens the pull request the run delivers (`gh auth login`).

If the server isn't running, the UI shows a clear banner with start instructions
rather than failing silently; REST/connection errors surface as toasts.

A run drives headless Claude Code with `--dangerously-skip-permissions` — a
non-interactive run has no human to approve tool calls. Agents work in throwaway
git worktrees, but that isolates the *git state*, not the OS: an agent can run
shell and read/write anything the candyland process can. Run it on your own
machine against your own repositories; treat the prompt as code you're executing.

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

Dev (UI hot-reload against the backend):

```bash
go run . &             # backend on :8888
npm run dev            # Vite dev server on :3000 → ooo-client talks to :8888
```

## Verify

```bash
go build ./... && go test ./...   # backend: compiles + unit tests (conductor delivery, httpapi)
npm run build                     # frontend builds
npm run validate                  # mermaid diagrams parse
npm run validate:layout           # no horizontal scroll / contained overflow (Playwright, preview)
```

The script-based checks drive the **real binary**, so build it once and point them at
it. They use a stub `claude` (`CANDYLAND_CLAUDE`) and stub `gh` (`CANDYLAND_GH`) so the
whole flow runs deterministically with **no Anthropic tokens and no GitHub** — only the
live Claude model behavior is left to a real run:

```bash
go build -o /tmp/candyland .
CANDYLAND_BIN=/tmp/candyland node scripts/check-system.mjs       # /api/system platform/deps
CANDYLAND_BIN=/tmp/candyland node scripts/check-workspaces.mjs   # workspace create/read/delete + folder validation
CANDYLAND_BIN=/tmp/candyland node scripts/check-history.mjs      # cancel keeps a run as "cancelled"; clear archives (kept in history)
CANDYLAND_BIN=/tmp/candyland node scripts/e2e.mjs                # full delivery: run → partition → worktrees → integrate → push → PR
CANDYLAND_BIN=/tmp/candyland node scripts/validate-flows.mjs     # browser: pick a folder, generated questions, cancel, clear, Tasks history (Playwright)
```

To exercise a **real** run end to end, build the binary, make sure `claude`, `git`, and
`gh` are installed and authenticated, and start a run from the UI against a workspace
pointing at one of your repositories.

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
