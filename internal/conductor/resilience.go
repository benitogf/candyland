package conductor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// claudeEnv is the environment for a spawned claude process. Claude Code refuses
// --dangerously-skip-permissions under root unless it believes it's sandboxed; a
// candyland run is headless by design (no human to approve tools), and the common
// WSL/server setup runs as root — so signal IS_SANDBOX there, or every run dies
// at the tech lead with "cannot be used with root/sudo privileges". Non-root runs
// are left untouched (the flag works as-is). os.Geteuid() is -1 on Windows.
func claudeEnv() []string {
	env := os.Environ()
	if os.Geteuid() == 0 {
		env = append(env, "IS_SANDBOX=1")
	}
	return env
}

// Resilience makes runs survive the ways a headless LLM process misbehaves:
// failing to start, hanging with no output, crashing, or — most commonly —
// "completing" without doing the work (deferring to a later step, or asking the
// user a question a non-interactive run can't answer). Real runs use the defaults
// below; tests shrink them via env so every path exercises quickly.

func envDur(key string, defMS int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(defMS) * time.Millisecond
}

// firstLine returns the first non-empty line of s (claude prints the key error on
// its own line; the rest is usually a stack/usage dump).
func firstLine(s string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// stallTimeout: kill a process that produces NO stream output for this long —
// only meant to catch a genuinely hung/deadlocked process, not honest work.
// Claude Code emits one stream-json line when it decides a tool call, then is
// silent on stdout until that single tool returns; a slow tool (a cold `go test
// ./...`, `npm ci`, a playwright run) can legitimately produce no output for
// minutes. So the default is deliberately generous and MUST exceed the slowest
// single tool the agents run — tune it with CANDYLAND_AGENT_STALL_MS. The real
// ceiling on a stuck attempt is attemptTimeout, below.
//
// attemptTimeout: hard wall-clock ceiling for one attempt (aligned with the ooo
// server's 20-minute read/write/idle timeouts). maxAttempts: total tries before
// an agent is declared failed.
func stallTimeout() time.Duration   { return envDur("CANDYLAND_AGENT_STALL_MS", 5*60*1000) }
func attemptTimeout() time.Duration { return envDur("CANDYLAND_AGENT_TIMEOUT_MS", 20*60*1000) }
func maxAttempts() int              { return envInt("CANDYLAND_AGENT_ATTEMPTS", 3) }

// maxReplans bounds the TOTAL number of partition attempts — the initial plan plus
// every reassessment — when the tech lead's own plan fails (a coder can't finish,
// or slices conflict and can't be reconciled in place). So the default 3 means one
// initial plan and up to two re-partitions before the run fails honestly. Each
// attempt re-runs the whole partition→code→integrate flow, so the bound is small.
// The run should recover from its own bad split — not die on it — but still
// converge. Tunable via CANDYLAND_REPLAN_ATTEMPTS.
func maxReplans() int { return envInt("CANDYLAND_REPLAN_ATTEMPTS", 3) }

// maxReviewRounds bounds the review→fix→re-review loop run AFTER integration and
// BEFORE any PR opens: the initial review plus every fix-then-re-review cycle. The
// default 10 means one review and up to nine fix-then-re-review cycles before the
// run fails honestly with the findings still open (no PR on un-reviewed work).
// Tunable via CANDYLAND_REVIEW_ROUNDS. The ceiling exists to stop a malfunctioning
// loop (thrash, non-convergence); it does not bound legitimate convergence — a
// rigorous review legitimately consumes several rounds.
func maxReviewRounds() int { return envInt("CANDYLAND_REVIEW_ROUNDS", 10) }

// startFailurePrefix marks the one run error that is ENVIRONMENTAL rather than a
// fault of the tech lead's plan: the claude binary couldn't even start (missing or
// unauthenticated). Re-partitioning can't fix that, so attemptDelivery treats a
// coder error with this prefix as terminal instead of a reason to reassess. The
// producer (below) and the detector (attemptDelivery) share this constant so the
// classification isn't a fragile substring guess.
const startFailurePrefix = "Claude Code failed to start: "

// resumeContinuePrompt is the prompt used when a limit/connection death is resumed
// on the ACTUAL work session (not the doctrine template): the interrupted work is
// continued in place rather than redone from scratch, so the agent finishes what it
// started and emits its required protocol/verdict line.
const resumeContinuePrompt = "You were interrupted (usage limit or connection loss) and this session has been resumed. Continue from where you stopped. Finish the task and emit the required protocol/verdict line."

// attemptOutcome is what one claude process run produced — enough to decide
// whether it actually complied with its instructions.
type attemptOutcome struct {
	partition     []partitionTask
	sawTool       bool   // the model used at least one tool (i.e. did real work)
	lastText      string // most recent assistant/result text (for deferral/question detection)
	stalled       bool   // killed for producing no output, or exceeding the wall clock
	startErr      error  // process could not be started (binary missing / not authenticated)
	runErr        error  // process exited non-zero on its own
	resultErrored bool   // a result line arrived with a non-success subtype (harness-signaled failure, even on a clean exit)
	stderr        string // the process's stderr (why it exited), surfaced on failure
	tokens        int    // output tokens reported on the result line (for callers with no tracked run, e.g. a quest tick)
	allText       string // every assistant/result text block joined (a verdict line may be in any block, not just the last)
	// Raw usage totals accumulated from result lines — UNSCALED counts, unlike
	// tokens above which keeps its /1000 display scaling. Input vs cache-read vs
	// cache-creation split so cost and cache efficiency are derivable per attempt.
	inputTokens      int
	cacheReadTokens  int
	cacheWriteTokens int
	// sessionID is the claude session id this spawn actually ran under — the first
	// non-empty session_id the stream emitted (the init line). It is the ACTUAL work
	// session, distinct from a forked template id, so a limit/connection resume can
	// continue the real interrupted work rather than redo it from the template.
	sessionID string
}

// terminalFailed reports that the spawn reached a NON-success terminal: it failed
// to start, exited non-zero, stalled, or the harness emitted a result line with a
// non-success subtype. A successful spawn (clean exit, success subtype) is never a
// death — so the limit/connection classifiers may trust its result text only when
// this is true, never misreading a completed verdict that merely quotes an error.
func (out attemptOutcome) terminalFailed() bool {
	return out.startErr != nil || out.runErr != nil || out.stalled || out.resultErrored
}

// spawnOpts are optional per-spawn knobs for streamOnce. The zero value is
// today's behavior for every existing caller, so the options are purely additive.
type spawnOpts struct {
	// maxTurns hard-caps the agentic turns claude takes in this one non-interactive
	// run (claude's --max-turns). 0 means NO cap — the historical behavior, kept for
	// the tech-lead/coder/conflict spawns. The review/fix identity passes a real
	// ceiling here so a context-blind pass cannot run away (C3).
	maxTurns int
	// model / thinking are the effective per-role selection (settings §9), threaded
	// from agentConfig(role) at each spawn site. Empty model falls back to the
	// default; empty thinking omits the --effort flag. model → claude --model,
	// thinking → claude --effort (the version-verified headless thinking control).
	model    string
	thinking string
	// forkFrom is a template session id: when non-empty the spawn FORKS that
	// session (claude --resume <id> --fork-session) instead of starting cold, so
	// the agent begins with the template's context already loaded. Empty is
	// today's cold-start behavior for every existing caller.
	forkFrom string
	// fallbackPrompt is the full bootstrap to rerun with when the fork didn't
	// resolve (the template session is gone). Only meaningful with forkFrom; the
	// rerun happens ONCE, inside the same attempt (see streamOnce).
	fallbackPrompt string
	// onForkUnresolved fires when forkFrom was set and the fork failed to
	// resolve — the spawn site uses it to drop the registry entry so later
	// spawns recreate the template instead of re-paying the doomed fork.
	// Called at most once per streamOnce, before the fallback rerun.
	onForkUnresolved func()
	// sessionID names this fresh spawn's claude session (--session-id) so the
	// usage-limit gate can resume the SAME session in place after a limit pause.
	// streamOnce mints one when a caller leaves it empty on a cold (non-fork,
	// non-resume) spawn; a fork spawn already carries the template's session.
	sessionID string
	// resumeFrom continues an existing session in place (claude --resume <id>,
	// WITHOUT --fork-session — unlike forkFrom, which forks). streamOnce sets it to
	// resume a limit-interrupted spawn; empty is a fresh spawn.
	resumeFrom string
}

// claudeArgs builds the argv for one claude spawn. It is a pure, separately
// testable function (no process, no I/O) so a test can assert the review/fix
// identity gets a real --max-turns cap on its argv, and that a fork spawn
// carries the paired fork args. busCfg, when non-empty, wires the coordination
// bus via --mcp-config. o carries the per-spawn knobs (maxTurns>0 appends the
// hard turn cap; forkFrom forks a template session); a zero o is today's argv
// byte-for-byte — the fork kill switch depends on that.
func claudeArgs(prompt string, extraDirs []string, busCfg string, o spawnOpts) []string {
	// `-p <prompt>` MUST stay first (the stub reads $2); the configured --model is
	// appended AFTER it, never before. An empty model falls back to the default.
	model := o.model
	if model == "" {
		model = defaultModel
	}
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--model", model, "--dangerously-skip-permissions"}
	// thinking maps to claude's --effort (verified against the installed CLI: it
	// accepts low|medium|high). Empty leaves it to claude's own default.
	if o.thinking != "" {
		args = append(args, "--effort", o.thinking)
	}
	// Fork a doctrine-loaded template session instead of starting cold. The two
	// flags travel together as an atomic pair: a bare --resume would CONTINUE the
	// template session in place and corrupt it for every later spawn.
	if o.forkFrom != "" {
		args = append(args, "--resume", o.forkFrom, "--fork-session")
	}
	// Resume THIS agent's own interrupted session in place after a usage-limit
	// pause: --resume alone (no --fork-session) continues it. Mutually exclusive
	// with forkFrom (a resume never also forks).
	if o.resumeFrom != "" {
		args = append(args, "--resume", o.resumeFrom)
	}
	// Name a fresh session so a later limit pause can resume it. --session-id is
	// only valid on a new session, so never pair it with a resume/fork.
	if o.sessionID != "" && o.forkFrom == "" && o.resumeFrom == "" {
		args = append(args, "--session-id", o.sessionID)
	}
	for _, d := range extraDirs {
		args = append(args, "--add-dir", d)
	}
	// A real, hard per-pass containment: claude's --max-turns aborts the run after
	// this many agentic turns. Only set when a caller asks for it (review/fix);
	// 0 leaves the spawn uncapped, exactly as before.
	if o.maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(o.maxTurns))
	}
	if busCfg != "" {
		args = append(args, "--mcp-config", busCfg)
	}
	return args
}

// streamOnce runs a single claude process, streaming its stream-json into the
// agent's live ooo state, and reports what happened. The process is killed if it
// stalls (no output within stallTimeout), exceeds the per-attempt wall clock, or
// the parent run is stopped — and the whole process tree goes with it.
//
// workdir is where the agent runs (the repo for the tech lead, a per-task git
// worktree for a coder). extraDirs are the run's other folders, exposed to
// the agent via --add-dir. The agent runs with --dangerously-skip-permissions
// because a headless run has no human to approve tool use — without it a coder
// can't edit files and silently does nothing.
//
// opts carries optional per-spawn knobs (e.g. a hard --max-turns cap, or a
// template session to fork); omit it for the historical uncapped behavior used
// by the tech-lead/coder/conflict spawns.
func streamOnce(parentCtx context.Context, c *Conductor, id, agentID, prompt, workdir string, extraDirs []string, opts ...spawnOpts) attemptOutcome {
	// A parent (quest) host coalesces its coordinating-agent writes; flush
	// AND evict the buffer when the attempt ends so the stream boundary is durable
	// regardless of where the coalesce window fell, and no permanent per-id entry
	// lingers retaining the lead's per-token history for the process lifetime. The
	// next stream re-seeds from storage. A no-op for run ids. (The per-attempt
	// context is created inside the retry loop below, so a limit/connection pause
	// gets a fresh deadline on resume rather than a stale one.)
	defer c.flushAndEvictAgentWrites(id)

	var o spawnOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	// Coordination bus: give the agent the comms_*/graph_*/brief_get MCP tools
	// wired to the conductor's ooo bus as this agentID (no-op when no bus is
	// running). The agent reads its initial context (the plan/task spec) via
	// brief_get rather than from argv — so a large plan can't overflow the
	// command line. claudeArgs wires --mcp-config when busCfg is non-empty.
	busCfg := c.busMCPConfig(id, agentID)
	// Mint a session id for a cold (non-fork, non-resume) spawn so a usage-limit
	// pause can resume THIS exact session in place instead of starting over.
	if o.sessionID == "" && o.forkFrom == "" && o.resumeFrom == "" {
		if sid, err := newSessionID(); err == nil {
			o.sessionID = sid
		}
	}
	// resumeSession is the session a limit-interrupted resume continues in place.
	// Preference order (documented): the ACTUAL work session (captured from the
	// init line once a spawn runs) ▸ the fork template ▸ the minted cold id. It
	// starts at template/cold and is upgraded to the real work session below.
	origForkFrom := o.forkFrom
	resumeSession := firstNonEmpty(o.forkFrom, o.sessionID)
	repaused := 0    // usage-limit + connection-loss pauses combined (unbounded)
	infraStreak := 0 // CONSECUTIVE connection-loss deaths (resets on any other outcome)
	// resumeInPlace switches this spawn to continue the interrupted session next
	// iteration. When resumeSession is the ACTUAL work session (differs from the
	// original template id), continue it with resumeContinuePrompt — the interrupted
	// work is resumed, not redone. When only the template id is known (the init line
	// never arrived), keep today's behavior byte-for-byte: redo cold with the
	// fallback bootstrap so a cold-boot fallback still has full context.
	resumeInPlace := func() {
		if resumeSession != "" {
			o.resumeFrom, o.forkFrom = resumeSession, ""
			if resumeSession != origForkFrom {
				prompt = resumeContinuePrompt
			} else if o.fallbackPrompt != "" {
				prompt = o.fallbackPrompt
			}
		}
	}
	for {
		// Conductor-wide gate: block every spawn until the limit resets (or, for a
		// sustained outage, until the infra backoff window passes).
		c.awaitLimit(parentCtx)
		if parentCtx.Err() != nil {
			return attemptOutcome{}
		}
		if repaused > 0 {
			c.resumeFromLimit(id) // gate opened — flip this run back to running
		}
		// Recreate the attempt deadline AFTER the gate reopens. awaitLimit can block
		// for the full limit window (hours), so a ctx created before the pause would
		// already be Done here — killing the resume spawn on arrival.
		attemptCtx, cancel := context.WithTimeout(parentCtx, attemptTimeout())
		out := c.spawnWithForkFallback(attemptCtx, parentCtx, id, agentID, prompt, workdir, extraDirs, busCfg, o)
		cancel()
		// Upgrade the resume target to the ACTUAL work session once a spawn ran under
		// one — so a later limit/connection resume continues the real interrupted work
		// (resumeContinuePrompt) instead of redoing it from the template.
		if out.sessionID != "" {
			resumeSession = out.sessionID
		}
		// A usage-limit death is NOT the agent's fault, so it must not burn an
		// attempt: arm the conductor-wide gate, then resume this session in place
		// once it reopens. The re-pause counter is unbounded — a limit can recur.
		if resetAt, isLimit := classifyUsageLimit(out, time.Now()); isLimit {
			repaused++
			infraStreak = 0
			c.armLimit(id, resetAt)
			c.updateAgentHost(id, func(agents *[]run.Agent) {
				appendToAgentIn(agents, agentID, run.Event{T: "system", Text: fmt.Sprintf(
					"usage limit reached (pause %d) — resuming at %s", repaused, resetAt.UTC().Format(time.RFC3339))}, 0)
			})
			resumeInPlace()
			continue
		}
		// A connection/infrastructure death is likewise not the agent's fault: pause
		// with escalating backoff and resume in place rather than misreading it as
		// "produced no verdict". A single blip pauses only this run; a sustained
		// outage (>= infraGateThreshold consecutive) arms the fleet-wide gate.
		if classifyInfra(out) {
			repaused++
			infraStreak++
			backoff := infraBackoff(infraStreak)
			resumeAt := time.Now().Add(backoff)
			systemic := infraStreak >= infraGateThreshold
			if systemic {
				c.armInfra(id, resumeAt) // gate the whole fleet; top-of-loop awaitLimit waits it out
			} else {
				c.pauseInfraLocal(id, resumeAt) // this run only
			}
			c.updateAgentHost(id, func(agents *[]run.Agent) {
				appendToAgentIn(agents, agentID, run.Event{T: "system", Text: fmt.Sprintf(
					"connection lost (pause %d, streak %d) — retrying at %s", repaused, infraStreak, resumeAt.UTC().Format(time.RFC3339))}, 0)
			})
			resumeInPlace()
			if systemic {
				continue // fleet-gated: the awaitLimit at the top of the loop handles the wait
			}
			// Sporadic: no fleet gate, so wait the backoff locally before retrying.
			if !sleepCtx(parentCtx, backoff) {
				return attemptOutcome{}
			}
			c.resumeFromLimit(id)
			continue
		}
		// The attempt resolved (not a limit/infra pause): capture any self-acknowledged
		// incidents this agent reported in its transcript onto the host record. Every
		// agent — run coders/tech-lead, quest-lead — funnels through
		// here, so this is the single choke point where a non-terminal self-report is
		// persisted onto the run/quest the id belongs to.
		c.captureIncidents(id, agentID, out.allText)
		return out
	}
}

// sleepCtx waits for d or until ctx is cancelled. It reports true if the full
// duration elapsed, false if ctx was cancelled first (the caller should abort).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// spawnWithForkFallback runs one spawn and, when it forked or resumed a session
// that didn't resolve (the transcript is gone), reruns ONCE cold with the full
// fallback bootstrap inside the same attempt — the retry/limit accounting never
// sees the fallback as a separate attempt. The cold rerun carries no fork/resume,
// so it can't recurse.
func (c *Conductor) spawnWithForkFallback(attemptCtx, parentCtx context.Context, id, agentID, prompt, workdir string, extraDirs []string, busCfg string, o spawnOpts) attemptOutcome {
	out := spawnStream(attemptCtx, parentCtx, c, id, agentID, claudeArgs(prompt, extraDirs, busCfg, o), workdir, busCfg)
	resumed := firstNonEmpty(o.forkFrom, o.resumeFrom)
	if resumed == "" || !forkUnresolved(out) {
		return out
	}
	if o.forkFrom != "" && o.onForkUnresolved != nil {
		o.onForkUnresolved()
	}
	if o.fallbackPrompt == "" {
		return out
	}
	c.updateAgentHost(id, func(agents *[]run.Agent) {
		appendToAgentIn(agents, agentID, run.Event{T: "system", Text: "session resume from " + resumed + " failed — falling back to the full bootstrap"}, 0)
	})
	cold := o
	cold.forkFrom, cold.resumeFrom = "", ""
	fb := spawnStream(attemptCtx, parentCtx, c, id, agentID, claudeArgs(cold.fallbackPrompt, extraDirs, busCfg, cold), workdir, busCfg)
	// The failed fork's (rare) usage still belongs to this attempt's accounting.
	fb.tokens += out.tokens
	fb.inputTokens += out.inputTokens
	fb.cacheReadTokens += out.cacheReadTokens
	fb.cacheWriteTokens += out.cacheWriteTokens
	return fb
}

// forkUnresolved classifies WHY a fork spawn failed: only a start failure or an
// exit pointing at the missing template conversation means the resume didn't
// resolve (retry cold via the fallback); any other failure — a stall, a crash on
// the work itself — is an ordinary run failure the resilience retries own.
func forkUnresolved(out attemptOutcome) bool {
	if out.startErr != nil {
		return true
	}
	return out.runErr != nil && strings.Contains(out.stderr, "No conversation found")
}

// spawnStream runs ONE claude process to completion, streaming its stream-json
// into the agent's live ooo state, and reports what it produced. It owns the
// per-process kill watcher, stall timer, and scanner-drain discipline, scoped to
// a per-process context derived from attemptCtx — so a fallback rerun inside the
// same attempt re-establishes all three cleanly while still honoring the
// attempt's wall clock and the parent's stop.
func spawnStream(attemptCtx, parentCtx context.Context, c *Conductor, id, agentID string, args []string, workdir, busCfg string) attemptOutcome {
	procCtx, cancel := context.WithCancel(attemptCtx)
	defer cancel()
	cmd := exec.Command(claudeBin(), args...)
	cmd.Dir = workdir
	cmd.Env = claudeEnv()
	// Also expose the bus coordinates directly in the environment. The real
	// claude reaches the bus through the --mcp-config above; these let any
	// process locate its own brief without parsing that file (used by the test
	// stub, which stands in for the model and fetches its brief over HTTP). The
	// plan never rides here — only the address and the agent id.
	if busCfg != "" {
		cmd.Env = append(cmd.Env, "CANDYLAND_BUS_ADDR="+c.server.Address, "CANDYLAND_AGENT_ID="+agentID)
	}
	configureProc(cmd)
	// Capture stderr so a process that exits non-zero reports WHY (e.g. an
	// auth/model/permission error from claude) instead of a blank "exited".
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return attemptOutcome{startErr: err}
	}
	afterStart(cmd) // assign to the Windows job object so killTree drops the whole tree (no-op on Unix)
	// Kill the whole process tree the moment this process's run ends, for any
	// reason (attempt timeout, parent stop, or the loop below finishing).
	go func() {
		<-procCtx.Done()
		killTree(cmd)
	}()

	lines := make(chan []byte, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-procCtx.Done():
				return
			}
		}
	}()

	out := attemptOutcome{}
	stall := time.NewTimer(stallTimeout())
	defer stall.Stop()
loop:
	for {
		select {
		case b, ok := <-lines:
			if !ok {
				break loop // the process's output ended (it exited)
			}
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(stallTimeout())
			var line streamLine
			if json.Unmarshal(b, &line) != nil {
				continue
			}
			// Capture the ACTUAL work session id (first non-empty wins) — the init
			// line carries it. A later limit/connection resume continues THIS session
			// in place rather than redoing the work from a forked template.
			if out.sessionID == "" && line.SessionID != "" {
				out.sessionID = line.SessionID
			}
			if line.Type == "result" {
				out.tokens += line.Usage.OutputTokens / 1000 // same 1k scaling appendToAgent uses
				out.inputTokens += line.Usage.InputTokens
				out.cacheReadTokens += line.Usage.CacheReadInputTokens
				out.cacheWriteTokens += line.Usage.CacheCreationInputTokens
				// A non-success result subtype (e.g. error_during_execution) is the
				// harness signalling a failed terminal even when the process then
				// exits 0 — the honest "this spawn did not succeed" marker the
				// death classifiers gate on, so a successful spawn's result text is
				// never misread as a limit/connection death.
				if line.Subtype != "" && line.Subtype != "success" {
					out.resultErrored = true
				}
			}
			p, sawTool, text := mapAgentLine(c, id, agentID, line)
			if p != nil {
				out.partition = p
			}
			if sawTool {
				out.sawTool = true
			}
			if text != "" {
				out.lastText = text
				out.allText += text + "\n"
			}
		case <-stall.C:
			out.stalled = true
			break loop
		case <-procCtx.Done():
			// Only the attempt wall clock or a parent stop can fire here — this
			// function's own cancel runs strictly after the loop exits.
			if parentCtx.Err() == nil {
				out.stalled = true // per-attempt wall-clock timeout (not a user stop)
			}
			break loop
		}
	}
	cancel()          // ensure the kill watcher fires and the scanner unblocks
	for range lines { // drain until the scanner goroutine closes the channel...
	} // ...so cmd.Wait() runs only after all reads complete.
	werr := cmd.Wait()
	// A non-zero exit is only a genuine failure if WE didn't kill the process.
	if !out.stalled && parentCtx.Err() == nil {
		out.runErr = werr
		out.stderr = strings.TrimSpace(stderr.String())
	}
	return out
}

var (
	// Phrases that mean the model punted instead of finishing.
	deferralRe = regexp.MustCompile(`(?i)(\bi['’]?ll (defer|leave|handle|do|finish|complete|come back|tackle|address)\b|\bdefer(ring)? (this|that|it|to|the)\b|\bnext step\b|\bfor now\b|\bout of scope\b|\bin a (later|follow[- ]?up|separate)\b|\bleave (this|that|it|the rest) (for|to)\b|\bwill (be )?(done|handled|addressed) (later|next|separately))`)
	// Phrases that mean the model is waiting on a human a headless run doesn't have.
	questionRe = regexp.MustCompile(`(?i)(could you (please )?(clarify|confirm|provide|specify|tell|let)|can you (clarify|confirm|provide|specify)|which (option|approach|one|of|would)|should i\b|do you want\b|would you like\b|please (clarify|confirm|specify|advise|let me know)|let me know (if|how|which|whether|what|your))`)
)

// isDeferralOrQuestion reports whether the model's last words mean it stopped
// short — deferring the work, or asking the (absent) user a question.
func isDeferralOrQuestion(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "?") {
		return true
	}
	return deferralRe.MatchString(s) || questionRe.MatchString(s)
}

// compliant decides whether an attempt actually did its job. A tech lead must
// emit a partition; a coder must take at least one real action (use a tool).
//
// The deferral/question check is applied ONLY when the model took no action —
// that's the failure mode we care about (it talked, asked, or punted instead of
// working). A coder that DID use tools is trusted even if its wrap-up summary
// happens to end with a question or a scoping note ("…out of scope", "Want me to
// also…?"); judging finished, tool-backed work as a failure on prose alone would
// discard real edits — a false failure, just as dishonest as a false success.
func compliant(out attemptOutcome, isTechLead bool) (bool, string) {
	if out.stalled {
		return false, "stalled — no output within the time limit"
	}
	if out.runErr != nil {
		if out.stderr != "" {
			return false, "the claude process exited: " + truncate(firstLine(out.stderr), 200)
		}
		return false, "the claude process exited with an error (" + out.runErr.Error() + ")"
	}
	if isTechLead {
		if len(out.partition) == 0 {
			return false, "did not emit a task partition"
		}
		return true, ""
	}
	if !out.sawTool {
		if isDeferralOrQuestion(out.lastText) {
			return false, "asked a question or deferred instead of doing the work"
		}
		return false, "took no actions — no changes were made"
	}
	return true, ""
}

// reinforce hardens the prompt on a retry: forbid questions and deferral, and
// restate the one hard requirement (a partition for the tech lead, real edits
// for a coder).
func reinforce(prompt string, attempt int, isTechLead bool) string {
	if attempt <= 1 {
		return prompt
	}
	firm := "\n\n--- AUTONOMY REQUIRED ---\n" +
		"This is a non-interactive run: there is NO human available to answer questions. " +
		"Do not ask questions, request clarification, or wait for input — make reasonable assumptions and state them briefly. " +
		"Do not defer, punt, or leave any part 'for a later step'; complete the task fully in this run."
	if isTechLead {
		firm += " Output exactly one line beginning with `PARTITION ` followed by the JSON array, then stop."
	} else {
		firm += " Use tools to actually make the changes — explaining is not enough."
	}
	return prompt + firm
}

// runAgentResilient runs an agent's claude process with retries. A process that
// fails to START is terminal (retrying a missing/unauthenticated binary is
// futile). A stall, crash, or non-compliant result is retried with a firmer,
// more autonomous prompt up to maxAttempts. On final failure it marks the agent
// blocked and records an actionable run error — it never reports false success.
// workdir/extraDirs scope where the agent runs (see streamOnce). Returns the
// parsed partition (tech lead) or nil.
func runAgentResilient(parentCtx context.Context, c *Conductor, id, agentID, basePrompt string, isTechLead bool, workdir string, extraDirs []string, opts ...spawnOpts) []partitionTask {
	attempts := maxAttempts()
	reason := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		if parentCtx.Err() != nil {
			return nil // run stopped
		}
		if attempt > 1 {
			n, total, why := attempt, attempts, reason
			log.Printf("candyland: run %s %s retry %d/%d after: %s", id, agentID, n, total, why)
			c.Update(id, func(r *run.Run) {
				setAgentState(r, agentID, "retrying", fmt.Sprintf("retry %d/%d — %s", n, total, why))
				appendToAgent(r, agentID, run.Event{T: "system", Text: fmt.Sprintf("retry %d/%d after: %s", n, total, why)}, 0)
			})
		}
		out := streamOnce(parentCtx, c, id, agentID, reinforce(basePrompt, attempt, isTechLead), workdir, extraDirs, opts...)
		if parentCtx.Err() != nil {
			return out.partition // stopped mid-attempt — not a failure
		}
		if out.startErr != nil {
			msg := startFailurePrefix + out.startErr.Error() + ". Ensure it's installed and authenticated (run `claude` once interactively, or set ANTHROPIC_API_KEY). See Setup for install instructions."
			log.Printf("candyland: run %s %s could not start: %v", id, agentID, out.startErr)
			c.Update(id, func(r *run.Run) {
				appendToAgent(r, agentID, run.Event{T: "text", Text: msg}, 0)
				r.Error = msg
				setAgentState(r, agentID, "blocked", "could not start")
			})
			// E2: a start failure is a genuine capability blocker (§1) — the terminal
			// `blocked` must carry a schema-valid postmortem. The agent couldn't answer
			// (its process never started), so the conductor synthesises one (§3).
			c.attachRunPostmortem(id, c.blockerPostmortemFor(agentID, out.allText,
				"claude process could not start for "+agentID, out.startErr.Error(), attempt, ""))
			return nil
		}
		ok, why := compliant(out, isTechLead)
		if ok {
			return out.partition
		}
		reason = why
		if attempt >= attempts {
			// Tech-lead missing PARTITION: one bounded resume-and-re-ask before the
			// failure path, and a truthful limit-vs-clean postmortem reason.
			if isTechLead && why == "did not emit a task partition" {
				var m, th string
				if len(opts) > 0 {
					m, th = opts[0].model, opts[0].thinking
				}
				if rep, did := c.repairVerdict(parentCtx, id, agentID, out.sessionID, "PARTITION", workdir, extraDirs, m, th); did {
					if p := parsePartition(rep.allText); len(p) > 0 {
						return p
					}
					why = noVerdictReason(rep, agentID, "PARTITION")
				} else {
					why = noVerdictReason(out, agentID, "PARTITION")
				}
				reason = why
			}
			msg := failMessage(agentID, isTechLead, why, attempts)
			log.Printf("candyland: run %s %s failed after %d attempts: %s", id, agentID, attempts, why)
			c.Update(id, func(r *run.Run) {
				appendToAgent(r, agentID, run.Event{T: "text", Text: msg}, 0)
				r.Error = msg
				setAgentState(r, agentID, "blocked", why)
			})
			// E2: the agent is terminally blocked after exhausting its bounded retries.
			// Attach a schema-valid postmortem — prefer the agent's own POSTMORTEM line
			// (bounced back and re-synthesised if incomplete), else synthesise from the
			// attempt data (§3). The bounded retry loop above IS the reject-and-loop-back.
			c.attachRunPostmortem(id, c.blockerPostmortemFor(agentID, out.allText,
				"agent "+agentID+" could not complete: "+why, orDefault(out.stderr, why), attempts, ""))
			return out.partition // nil for a failed tech lead; fanOut treats empty as failure
		}
	}
	return nil
}

// repairVerdict re-asks an agent that completed without its protocol line to emit
// it, by resuming its OWN session (one bounded spawn). Returns the repaired
// outcome; ok=false when no session id is known or the repair also lacks the line.
// It is the "one bounded resume-and-re-ask" the doctrine requires before a unit
// blocks on a missing protocol line — the caller re-parses the returned allText.
func (c *Conductor) repairVerdict(ctx context.Context, hostID, agentID, sessionID,
	verdictName, workdir string, extra []string, model, thinking string) (attemptOutcome, bool) {
	if sessionID == "" {
		return attemptOutcome{}, false // no session to resume — nothing to re-ask
	}
	prompt := "Your previous reply did not include the required " + verdictName +
		" line. Based on the work you already completed in this session, emit it now. Output ONLY the protocol line."
	out := streamOnce(ctx, c, hostID, agentID, prompt, workdir, extra,
		spawnOpts{resumeFrom: sessionID, maxTurns: 2, model: model, thinking: thinking})
	return out, true
}

// terminalLimitInterrupted reports whether an outcome's terminal text matches a
// usage-limit banner/phrase — the honest signal that a missing verdict is a limit
// death, not agent noncompliance. Used by the truthful-postmortem call sites.
func terminalLimitInterrupted(out attemptOutcome) bool {
	text := out.stderr + "\n" + out.lastText + "\n" + out.allText
	return limitBannerRe.MatchString(text) || limitPhraseRe.MatchString(text)
}

// noVerdictReason classifies why an agent produced no verdict: a limit-interrupted
// terminal records the limit death; only a genuinely clean, non-limit terminal
// records "produced no <verdict> verdict" (so a postmortem never misattributes a
// limit death as noncompliance).
func noVerdictReason(out attemptOutcome, agentID, verdictName string) string {
	if terminalLimitInterrupted(out) {
		return "usage limit interrupted " + agentID + " before its " + verdictName + " verdict"
	}
	return "produced no " + verdictName + " verdict"
}

func failMessage(agentID string, isTechLead bool, why string, attempts int) string {
	who := "Agent " + agentID
	if isTechLead {
		who = "The tech lead"
	}
	return who + " could not complete after " + strconv.Itoa(attempts) + " attempts (" + why + "). " +
		"This usually means the task needs to be split smaller or stated more concretely — refine the prompt and start a new run."
}
