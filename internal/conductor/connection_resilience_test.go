package conductor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// The REAL usage-limit death is Claude Code's own banner, and it commonly arrives
// on a CLEAN exit (the banner is the process's final result, not an error). The
// pre-fix classifier gated on a process death and so missed it — the exact
// misclassification (the 2026-07-07 outage) that terminated live work as
// "produced no verdict". The banner must classify regardless of exit state, while
// the ambiguous quota phrases still require a death (so a reviewer's transcript
// mentioning them is not misread).
func TestClassifyUsageLimitBannerCleanExit(t *testing.T) {
	now := time.Date(2026, 7, 7, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		out  attemptOutcome
		want bool
	}{
		{"real banner, clean exit (no death)", attemptOutcome{sawTool: true, lastText: "You've hit your session limit · resets 3:40pm (Asia/Hong_Kong)"}, true},
		{"real banner in stderr (non-zero exit)", attemptOutcome{runErr: errStub, stderr: "You've hit your usage limit · resets 2:10am (Asia/Hong_Kong)"}, true},
		{"banner in a non-success result subtype (clean exit)", attemptOutcome{resultErrored: true, lastText: "You've hit your session limit · resets 3:40pm (Asia/Hong_Kong)"}, true},
		{"banner curly apostrophe", attemptOutcome{lastText: "You’ve hit your session limit · resets 9am"}, true},
		{"ambiguous phrase without a death is NOT a limit", attemptOutcome{sawTool: true, lastText: "I reviewed the usage limit reached handling and the 429 path"}, false},
		{"ambiguous phrase WITH a death is a limit", attemptOutcome{runErr: errStub, stderr: "usage limit reached"}, true},
		{"ordinary clean success", attemptOutcome{sawTool: true, lastText: "REVIEW_CLEAN all good"}, false},
		// The bug case: a SUCCESSFUL reviewer whose long verdict quotes the banner
		// (e.g. auditing this very file) must NOT be misread as a limit and retried.
		{"successful long verdict quoting the banner is NOT a limit", attemptOutcome{sawTool: true, lastText: "REVIEW_CLEAN. The limitBannerRe correctly matches \"You've hit your session limit\" as the harness banner, and the classifier gates on a genuine death so a successful spawn quoting it is not misread. No blockers across this substantial diff touching the spawn-retry core; the change is wired into streamOnce."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := classifyUsageLimit(tc.out, now); ok != tc.want {
				t.Fatalf("classifyUsageLimit ok=%v want %v", ok, tc.want)
			}
		})
	}
}

// The banner carries its reset time in a named timezone in parentheses; it must be
// honored via time.LoadLocation, not interpreted in server-local time (a dropped
// timezone is one of the four pre-fix classifier gaps).
func TestParseResetTimeTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Skip("no tzdata on this host — LoadLocation('Asia/Hong_Kong') unavailable")
	}
	// now = 01:00Z = 09:00 HKT on 2026-07-07. "resets 3:40pm (Asia/Hong_Kong)" =
	// 15:40 HKT = 07:40Z, still ahead of now → today.
	now := time.Date(2026, 7, 7, 1, 0, 0, 0, time.UTC)
	got, ok := parseResetTime("You've hit your session limit · resets 3:40pm (Asia/Hong_Kong)", now)
	if !ok {
		t.Fatal("expected a parseable reset time")
	}
	want := time.Date(2026, 7, 7, 15, 40, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("tz reset got %v want %v", got.UTC(), want.UTC())
	}
	// A wall-clock already past in that zone rolls to tomorrow: now = 09:00Z = 17:00
	// HKT, so 15:40 HKT today already passed → +24h.
	now2 := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	got2, _ := parseResetTime("resets 3:40pm (Asia/Hong_Kong)", now2)
	want2 := time.Date(2026, 7, 8, 15, 40, 0, 0, loc)
	if !got2.Equal(want2) {
		t.Fatalf("tz rollover got %v want %v", got2.UTC(), want2.UTC())
	}
	// An unknown timezone falls back to now's location rather than failing.
	got3, ok3 := parseResetTime("resets 3pm (Not/AZone)", now)
	if !ok3 || !got3.Equal(time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("unknown-tz fallback got %v ok=%v", got3, ok3)
	}
}

// A connection/infrastructure death is a distinct class from a usage limit and from
// an agent's own bad output: it must be recognized from concrete transport signals
// (the ones observed in the c4/c5 outage) but NOT swallow a bare crash or a stall,
// which still surface honestly.
func TestClassifyInfra(t *testing.T) {
	cases := []struct {
		name string
		out  attemptOutcome
		want bool
	}{
		// Real infra deaths carry a death signal (non-zero exit → runErr+stderr, or a
		// non-success result subtype). The connection errors below are the ones
		// observed in the 2026-07-07 outage; each is in the process stderr.
		{"unable to connect (ConnectionRefused)", attemptOutcome{runErr: errStub, stderr: "API Error: Unable to connect to API (ConnectionRefused)"}, true},
		{"auth 401 flap", attemptOutcome{runErr: errStub, stderr: "Failed to authenticate. API Error: 401 Invalid authentication credentials"}, true},
		{"connection refused in stderr with exit", attemptOutcome{runErr: errStub, stderr: "dial tcp 1.2.3.4:443: connection refused"}, true},
		{"server 5xx overloaded", attemptOutcome{runErr: errStub, stderr: "API Error: 529 overloaded_error"}, true},
		{"fetch failed", attemptOutcome{runErr: errStub, stderr: "fetch failed"}, true},
		// Clean exit (0) but a non-success result subtype: the error text is in the
		// result, and terminalFailed (resultErrored) makes it trustworthy.
		{"non-success subtype carries the error in the result", attemptOutcome{resultErrored: true, lastText: "API Error: Unable to connect to API (ConnectionRefused)"}, true},
		{"bare crash, no network text — surfaces honestly", attemptOutcome{runErr: errStub, stderr: "panic: runtime error: invalid memory address"}, false},
		{"pure stall — honest fail, not infra", attemptOutcome{stalled: true}, false},
		{"clean success", attemptOutcome{sawTool: true, lastText: "done"}, false},
		{"usage-limit banner is NOT infra", attemptOutcome{runErr: errStub, stderr: "You've hit your session limit · resets 3pm"}, false},
		// The bug cases: a SUCCESSFUL spawn whose result merely MENTIONS network errors
		// (a reviewer auditing this file, a coder summarizing an HTTP failure) must NOT
		// be misclassified as infra and retried forever — regardless of result length.
		{"successful long verdict mentioning network errors is NOT infra", attemptOutcome{sawTool: true, lastText: "REVIEW_CLEAN. I audited internal/conductor/limit.go: infraDeathRe matches 'connection reset', 'api error: 401', '529', 'fetch failed', and 'no such host', and the classifier gates on a genuine death so a successful spawn quoting these tokens in its verdict is not misread. All acceptance criteria satisfied; the change is wired into streamOnce and verified green."}, false},
		{"short clean success mentioning a token is NOT infra", attemptOutcome{sawTool: true, lastText: "REVIEW_CLEAN — the 529 backoff is handled correctly"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInfra(tc.out); got != tc.want {
				t.Fatalf("classifyInfra=%v want %v", got, tc.want)
			}
		})
	}
}

// Tokens burned by a dead resume leg are really spent, so the resolving outcome
// must report the SUM across every leg — not just the final one. A first leg
// that emits usage then dies on a connection error, resumed by a green second
// leg, must return the combined usage; dropping the first leg silently
// understates the cost of every interrupted attempt.
func TestResumeLegsMergeUsage(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "leg1-done")
	// Leg 1 does real work (a tool_use) and self-reports an INCIDENT before dying;
	// leg 2 resumes with NO tool_use and different text. This shapes the r123 case:
	// the merged outcome must OR sawTool (true from leg 1) and JOIN allText (so the
	// pre-pause self-report survives for captureIncidents) — not just sum tokens.
	stub := "#!/usr/bin/env bash\n" +
		"if [[ ! -f \"" + marker + "\" ]]; then\n" +
		"  touch \"" + marker + "\"\n" +
		"  echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"x\"}}]}}'\n" +
		"  echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"INCIDENT pre-pause self-report from leg1\"}]}}'\n" +
		"  echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"leg1\",\"usage\":{\"output_tokens\":2000,\"input_tokens\":1000,\"cache_read_input_tokens\":10,\"cache_creation_input_tokens\":20}}'\n" +
		"  echo 'API Error: Unable to connect to API (ConnectionRefused)' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"resumed green\",\"usage\":{\"output_tokens\":3000,\"input_tokens\":500,\"cache_read_input_tokens\":5,\"cache_creation_input_tokens\":7}}'\n"
	writeFakeClaude(t, stub)
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "2000")
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")
	t.Setenv("CANDYLAND_INFRA_BACKOFF_MS", "5")

	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan attemptOutcome, 1)
	go func() { done <- streamOnce(ctx, c, id, "a", "go on", t.TempDir(), nil) }()

	var out attemptOutcome
	select {
	case out = <-done:
	case <-ctx.Done():
		t.Fatal("streamOnce never returned — the infra retry likely hung")
	}

	// output_tokens are /1000-scaled: (2000+3000)/1000 = 5.
	if out.tokens != 5 {
		t.Errorf("tokens = %d, want 5 (both legs summed)", out.tokens)
	}
	if out.inputTokens != 1500 {
		t.Errorf("inputTokens = %d, want 1500 (1000+500)", out.inputTokens)
	}
	if out.cacheReadTokens != 15 {
		t.Errorf("cacheReadTokens = %d, want 15 (10+5)", out.cacheReadTokens)
	}
	if out.cacheWriteTokens != 27 {
		t.Errorf("cacheWriteTokens = %d, want 27 (20+7)", out.cacheWriteTokens)
	}
	// sawTool must OR across legs: only leg 1 used a tool, yet the merged outcome
	// must report true — the r123 defect was the final (tool-less) resume leg
	// masking the pre-pause work and triggering a false "made no changes" refusal.
	if !out.sawTool {
		t.Error("sawTool = false, want true (leg 1 used a tool; OR must survive the resume)")
	}
	// allText must JOIN across legs so captureIncidents still sees the pre-pause
	// self-report that leg 1 emitted before the connection died.
	if !strings.Contains(out.allText, "INCIDENT pre-pause self-report from leg1") {
		t.Errorf("allText = %q, want it to retain leg 1's pre-pause self-report", out.allText)
	}
	if !strings.Contains(out.allText, "resumed green") {
		t.Errorf("allText = %q, want it to include leg 2's text", out.allText)
	}
}

// A single sporadic connection loss pauses only its own run; a sustained outage
// (>= infraGateThreshold consecutive) arms the conductor-wide gate that blocks every
// spawn — the fleet-wide half, mirroring the usage-limit gate.
func TestInfraSporadicVsSystemicGate(t *testing.T) {
	c := New(nil)
	id := c.Create(run.Spec{Prompt: "x"})
	future := time.Now().Add(time.Hour)

	c.pauseInfraLocal(id, future)
	if d := c.limitDeadline(); !d.IsZero() {
		t.Fatalf("a sporadic infra pause must NOT arm the fleet gate, got deadline %v", d)
	}
	r, _ := c.Get(id)
	if r.Status != "paused" || r.PauseReason != "connection lost — retrying" {
		t.Fatalf("sporadic pause not persisted: status=%q reason=%q", r.Status, r.PauseReason)
	}

	c.armInfra(id, future)
	if d := c.limitDeadline(); !d.Equal(future) {
		t.Fatalf("a systemic infra pause must arm the fleet gate to %v, got %v", future, d)
	}
}

// End-to-end through streamOnce: a spawn that dies with a connection error must
// pause+resume in place and complete green — never returning the death for a caller
// to misread as "produced no verdict". Because EVERY coordinator/run spawn routes
// through streamOnce, this is the proof that the misclassification sites are covered
// by construction (a limit/infra death never escapes this choke point).
func TestInfraDeathPausesAndResumes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "infra-hit")
	stub := "#!/usr/bin/env bash\n" +
		"if [[ ! -f \"" + marker + "\" ]]; then\n" +
		"  touch \"" + marker + "\"\n" +
		"  echo 'API Error: Unable to connect to API (ConnectionRefused)' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file\":\"x\"}}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"resumed green\",\"usage\":{\"output_tokens\":3}}'\n"
	writeFakeClaude(t, stub)
	t.Setenv("CANDYLAND_AGENT_TIMEOUT_MS", "2000")
	t.Setenv("CANDYLAND_AGENT_STALL_MS", "10000")
	t.Setenv("CANDYLAND_INFRA_BACKOFF_MS", "5") // collapse the 1m first backoff

	c := New(nil)
	id := c.Create(run.Spec{Prompt: "do the thing"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan attemptOutcome, 1)
	go func() { done <- streamOnce(ctx, c, id, "a", "go on", t.TempDir(), nil) }()

	var out attemptOutcome
	select {
	case out = <-done:
	case <-ctx.Done():
		t.Fatal("streamOnce never returned — the infra retry likely hung")
	}

	if out.startErr != nil || out.runErr != nil || out.stalled {
		t.Fatalf("infra death was not absorbed — spawn surfaced as a failure: startErr=%v runErr=%v stalled=%v", out.startErr, out.runErr, out.stalled)
	}
	if !out.sawTool {
		t.Error("resumed spawn did no work — infra pause+resume path not exercised")
	}

	r, _ := c.Get(id)
	paused := false
	for _, a := range r.Agents {
		if a.ID != "a" {
			continue
		}
		for _, e := range a.Events {
			if strings.Contains(e.Text, "connection lost (pause") {
				paused = true
			}
		}
	}
	if !paused {
		t.Error("expected a 'connection lost' pause event in the agent stream")
	}
	if r.ResumeAt != "" || r.PauseReason != "" {
		t.Errorf("resume must clear the pause markers, got resumeAt=%q pauseReason=%q", r.ResumeAt, r.PauseReason)
	}
}
