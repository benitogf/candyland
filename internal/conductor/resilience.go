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
	for _, ln := range strings.Split(s, "\n") {
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

// startFailurePrefix marks the one run error that is ENVIRONMENTAL rather than a
// fault of the tech lead's plan: the claude binary couldn't even start (missing or
// unauthenticated). Re-partitioning can't fix that, so attemptDelivery treats a
// coder error with this prefix as terminal instead of a reason to reassess. The
// producer (below) and the detector (attemptDelivery) share this constant so the
// classification isn't a fragile substring guess.
const startFailurePrefix = "Claude Code failed to start: "

// attemptOutcome is what one claude process run produced — enough to decide
// whether it actually complied with its instructions.
type attemptOutcome struct {
	partition []partitionTask
	sawTool   bool   // the model used at least one tool (i.e. did real work)
	lastText  string // most recent assistant/result text (for deferral/question detection)
	stalled   bool   // killed for producing no output, or exceeding the wall clock
	startErr  error  // process could not be started (binary missing / not authenticated)
	runErr    error  // process exited non-zero on its own
	stderr    string // the process's stderr (why it exited), surfaced on failure
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
func streamOnce(parentCtx context.Context, c *Conductor, id, agentID, prompt, workdir string, extraDirs []string) attemptOutcome {
	attemptCtx, cancel := context.WithTimeout(parentCtx, attemptTimeout())
	defer cancel()

	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--model", "claude-opus-4-8", "--dangerously-skip-permissions"}
	for _, d := range extraDirs {
		args = append(args, "--add-dir", d)
	}
	// Coordination bus: give the agent the comms_*/graph_*/brief_get MCP tools
	// wired to the conductor's ooo bus as this agentID (no-op when no bus is
	// running). The agent reads its initial context (the plan/task spec) via
	// brief_get rather than from argv — so a large plan can't overflow the
	// command line.
	busCfg := c.busMCPConfig(id, agentID)
	if busCfg != "" {
		args = append(args, "--mcp-config", busCfg)
	}
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
	// Kill the whole process tree the moment the attempt ends, for any reason.
	go func() {
		<-attemptCtx.Done()
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
			case <-attemptCtx.Done():
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
			p, sawTool, text := mapAgentLine(c, id, agentID, line)
			if p != nil {
				out.partition = p
			}
			if sawTool {
				out.sawTool = true
			}
			if text != "" {
				out.lastText = text
			}
		case <-stall.C:
			out.stalled = true
			break loop
		case <-attemptCtx.Done():
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
func runAgentResilient(parentCtx context.Context, c *Conductor, id, agentID, basePrompt string, isTechLead bool, workdir string, extraDirs []string) []partitionTask {
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
		out := streamOnce(parentCtx, c, id, agentID, reinforce(basePrompt, attempt, isTechLead), workdir, extraDirs)
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
			return nil
		}
		ok, why := compliant(out, isTechLead)
		if ok {
			return out.partition
		}
		reason = why
		if attempt >= attempts {
			msg := failMessage(agentID, isTechLead, why, attempts)
			log.Printf("candyland: run %s %s failed after %d attempts: %s", id, agentID, attempts, why)
			c.Update(id, func(r *run.Run) {
				appendToAgent(r, agentID, run.Event{T: "text", Text: msg}, 0)
				r.Error = msg
				setAgentState(r, agentID, "blocked", why)
			})
			return out.partition // nil for a failed tech lead; fanOut treats empty as failure
		}
	}
	return nil
}

func failMessage(agentID string, isTechLead bool, why string, attempts int) string {
	who := "Agent " + agentID
	if isTechLead {
		who = "The tech lead"
	}
	return who + " could not complete after " + strconv.Itoa(attempts) + " attempts (" + why + "). " +
		"This usually means the task needs to be split smaller or stated more concretely — refine the prompt and start a new run."
}
