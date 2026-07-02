# 🍬 Candyland

An **observe-only sidecar** for [detritus](https://github.com/benitogf/detritus)'s
autonomous build flows. You launch a run, quest, or campaign from your detritus
session; candyland drives the agents out of process and shows every agent's live
state, output, and trace — so you watch (and stop) the work in a dashboard instead
of juggling sessions by hand, and review one finished PR.

A single standalone binary: a Go [ooo](https://github.com/benitogf/ooo) realtime
backend that embeds and serves the built React UI, built on the
[mono](https://github.com/benitogf/mono) boilerplate.

## What it is (and isn't)

Candyland is **not** something you start work from. There is no in-UI wizard, no
"launch from your editor" button, no plan phase, no developer/non-developer modes.
The user session — driven by detritus — owns intake and planning; candyland is the
window into the build. The dashboard's job is observation and a single coarse
control: **stop**.

## Process model

One candyland app process serves everything:

- the **ooo realtime bus** (live state the UI subscribes to over WebSocket),
- the **React UI** (on `--spaPort`, default `:8080`),
- the **REST API** (on `--port`, default `:8888`) detritus drives, and
- the **HTTP coordination MCP** at `/mcp/comms/{agentID}` — the back-channel
  spawned agents use to coordinate.

The only other processes are **one `claude` per active agent** (a tech lead, the
coders, the reviewers), spawned by the conductor as headless
`claude -p … --output-format stream-json`. There is **no per-session control MCP**
and **no per-agent comms MCP process** — the comms tools are served by the single
app process over HTTP, keyed by the agent id in the path.

```
detritus session ──REST (/api/runs|quests|campaigns)──▶  candyland app process
                                                          ├─ ooo bus  ◀──ws── React UI
                                                          ├─ REST API
                                                          ├─ HTTP MCP /mcp/comms/{agentID}
                                                          └─ conductor ──spawns──▶ claude (one per agent)
                                                                                   in git worktrees
                                                                                   → integrate → review
                                                                                   → push → open PR
```

## The work hierarchy

Everything candyland tracks is one of three first-class records; the single
**Work** UI section pivots between them (Runs/Tasks · Quests · Campaigns) with
filters.

- **Run** — one bounded build. A tech lead partitions the work; one coder per
  fork-safe task runs in its own git worktree; the diffs integrate; a reviewer
  passes; one PR per impacted repo opens.
- **Quest** — a long-running objective that launches **many runs**. Its quest
  lead discovers and triages work items, launches a child run per accepted item,
  and finishes when no safe work is left (with token-budget and pause/stop gates).
- **Campaign** — a program-level intent. Its supervisor produces an **intent
  brief** (restated goal + commitments), passes a **brief gate** and a **plan
  gate**, decomposes into quests/runs that deliver onto a shared branch, then
  runs a **final intent review** (a per-commitment `satisfied|partial|missed`
  verdict). A `missed` commitment **blocks** that repo's PR; otherwise it opens
  **one PR per repo**.

Launched from the detritus session over REST:

```bash
detritus --candyland-run <prompt-file> [folder ...]    # a build run
detritus --quest-run     <objective-file> [folder ...] # an iterative quest
detritus --campaign-run  <input-file> [folder ...]     # a program campaign
```

## The review loop

A run does not open a PR on the coders' word. After the worktrees integrate, a
**separate reviewer agent** hard-reviews each repo's integrated diff — loading the
detritus review doctrine (`core/review-rigor` + truthseeker) via `kb_get`, not an
inlined rubric. The reviewer emits a structured verdict: `REVIEW_CLEAN` (no
blockers) or `REVIEW_FINDINGS {…}` with cited blockers, which route back for
another round. **A PR opens only when the review is clean.**

## Traces

Every run, quest, and campaign carries a stable id, parent links (a child run's
`questId`/`campaignId`; a quest's `campaignId`), and a versioned trace schema
(`traceVersion`). A run's full normalized trace — the run plus its audit — is
exportable locally:

```
GET /api/runs/{id}/trace
```

Traces are local-first; a redaction seam (`run.RunTrace`) is reserved for any
future sync.

## Data

State lives at **`~/.candyland/`** (the db under `~/.candyland/db`), not inside any
project. An explicit `--dataPath` overrides it; an unset path resolves to the home
directory and migrates a legacy project-local `./db` on a best-effort basis.

## Install & lifecycle

**Detritus owns candyland end to end.** `detritus --setup` fetches the released
candyland binary (pure Go — no shell installer) and places it beside detritus;
detritus then ensures the app is up and drives it over REST. There is no
`install.sh`, no `install.ps1`, and no separate MCP registration.

## Platforms

Runs on **Linux, macOS, WSL, and Windows** — a single self-contained binary per
OS/arch. On **Linux/amd64 and Windows** the binary opens the dashboard in a
**native desktop window** (WebKitGTK 4.1 on Linux/WSLg, WebView2 on Windows; built
with `-tags webview`). Elsewhere — and with `--headless`, or when no display is
available — it serves the dashboard on `--spaPort` for a browser. The API/realtime
backend is identical either way; the window is just a shell around the embedded SPA.

Dependencies a run needs:

- **Claude Code** (`claude`) — drives the agents. Without it a run **fails
  honestly** with an actionable error; there is no demo/scripted fallback.
- **git** — for the run branch and worktrees.
- **GitHub CLI** (`gh`) — opens the PR a run delivers (`gh auth login`).

The app binds to **loopback by default**: a run drives headless Claude with
`--dangerously-skip-permissions` (a non-interactive run has no human to approve
tool calls) and the API can browse the backend's filesystem, so it must not be on
the network unless you opt in with `--host 0.0.0.0`. Agents work in throwaway git
worktrees, but that isolates *git state*, not the OS. Run it on your own machine
against your own repositories; treat the prompt as code you're executing.

## Run (from source)

```bash
npm install
npm run build          # vite → ./build (embedded by the Go binary)
go run .               # UI on http://localhost:8080, realtime+API on :8888
```

Dev (UI hot-reload against the backend):

```bash
go run . &             # backend on :8888
npm run dev            # Vite dev server → ooo-client talks to :8888
```

## Verify

```bash
go build ./...                    # backend compiles (all platforms: add GOOS=windows)
go test ./...                     # backend unit + oracle tests (no model calls)
go vet ./...
npm run build                     # frontend builds
npm run validate                  # mermaid diagrams parse
```

### Deterministic regression tests (no model calls)

The conductor's run/quest/campaign flows are regression-tested with **no Anthropic
tokens**. A run spawns a **stub `claude`** (an executable bash script the executor
uses when `CANDYLAND_CLAUDE` is set) that speaks the real CLI contract: it is
invoked as `claude -p <prompt> …`, writes newline-delimited stream-json envelopes,
and signals each stage with the same fenced verdict lines a real agent emits —
`PARTITION` / `TEST` / `REVIEW_CLEAN` for a run, `WORKITEMS` for a quest lead,
`INTENT_BRIEF` / `INTENT_REVIEW` for a campaign supervisor. Because the spawned
process and the I/O contract are real, these tests exercise the genuine executor
(partition → worktrees → integrate → review → push → PR, and the quest/campaign
supervisor loops) — only the model's judgement is replaced by a deterministic script.

To write one, compose per-role fragments with the harness in
[`internal/conductor/stubclaude_test.go`](internal/conductor/stubclaude_test.go)
and hand the result to `deliveryConductor` (single repo), `multiRepoConductor`
(N repos), or the quest/campaign helpers:

```go
script := stubClaude(
    roleCleanReviewer,                              // "code reviewer" spawns → REVIEW_CLEAN
    role("tech lead", emitPartition(`[{"id":"a","files":["a.txt"],"test":"t"}]`)),
    coder(writeWorktreeFile("a.txt"), emitTest(1, 0)),  // default branch (any coder)
)
c, repo := deliveryConductor(t, script)
// … create + begin a run, then assert against c.Get(id)
```

Fragments dispatch on the spawn prompt in order; a keyword-less `coder(...)`
fragment is the default that catches every coder spawn. To script per-tick or
per-stage behaviour (e.g. fail a gate once then pass), branch inside a fragment on
a marker file whose path the test sets via `t.Setenv` — the `CANDYLAND_*_FIXTURE`
convention (`questTickClaude` / `campaignClaude` are worked oracles). The fenced
verdict conventions are pinned independently by the `parse*` tests, so a stub and a
real agent can't drift on the contract silently.

The script-based system checks drive the **real binary** with a stub `claude` and
stub `gh`, so the whole delivery runs deterministically:

```bash
go build -o /tmp/candyland .
CANDYLAND_BIN=/tmp/candyland node scripts/check-system.mjs    # /api/system platform/deps
CANDYLAND_BIN=/tmp/candyland node scripts/check-history.mjs   # cancel/clear/history
CANDYLAND_BIN=/tmp/candyland node scripts/e2e.mjs             # full delivery: run → … → PR
```

To exercise a **real** run end to end, build the binary, ensure `claude`, `git`,
and `gh` are installed and authenticated, and launch a run from your detritus
session naming one of your repositories as the run's folder.

## Releases

A manual bump/tag/release you trigger from a prompt ("release candyland X.Y.Z") or
directly:

```bash
scripts/release.sh 0.1.0   # from main, clean tree → tags v0.1.0 and pushes
```

The `v*` tag triggers the release workflow, which builds every binary (backend +
embedded UI, version injected via `-ldflags`) for linux/darwin/windows (amd64 +
arm64) from a single Linux runner via **Bazel + a Zig hermetic C toolchain** (the
[`spartan/samples/webcanvas`](https://github.com/benitogf/spartan) approach):
linux/amd64 + windows are CGO `-tags webview` builds (linux native against
WebKitGTK; windows cross-compiled with `zig cc` + a WebView2 shim), and
linux/arm64 + darwin are headless CGO-free server builds. It publishes the GitHub
Release `detritus --setup` pulls from. Build locally with `bazel build //:release`
(Go, Zig 0.13, `libwebkit2gtk-4.1-dev` required). The workflow lives at
[`ci/release.yml`](ci/release.yml) and must be activated once per
[`ci/README.md`](ci/README.md).

## Stack

| Layer    | Choice                                                          |
| -------- | --------------------------------------------------------------- |
| UI       | React 19, Vite 7, MUI 7                                         |
| Realtime | `ooo` + `ooo-client` over WebSocket                             |
| Backend  | Go (ooo server + conductor), `mono` boilerplate, single binary |
| Agents   | headless `claude -p --output-format stream-json`                |

Open **How it works** in the app for the full design.
