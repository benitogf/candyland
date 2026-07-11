package conductor

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/run"
)

// Usage-limit auto-resume. Claude Code dies (or emits a terminal message) when
// the account's usage limit is hit — a spawn that fails this way is NOT a fault
// of the agent's work, so it must not burn a retry. Instead the whole conductor
// pauses every spawn until the limit resets, then resumes the interrupted session
// in place. The gate is conductor-wide (one account, one limit) so a limit hit by
// any agent blocks all of them; it survives a restart via a paused run's persisted
// resumeAt (rehydrate re-arms the gate).

// defaultLimitBackoff is how long to wait when the death says a limit was hit but
// carries no parseable reset time — long enough to clear a per-minute rate spike
// without wedging a run for hours on an unparseable message.
const defaultLimitBackoff = time.Hour

var (
	// limitBannerRe matches Claude Code's OWN usage-limit banner — a string the
	// harness itself prints when the seat's quota is exhausted ("You've hit your
	// session limit · resets 3:40pm (Asia/Hong_Kong)"). Because only the harness
	// emits this exact phrasing (never an agent describing quota code), a match is a
	// real limit death REGARDLESS of exit state — real limit deaths often exit clean
	// with the banner as their final result rather than erroring, so gating on a
	// process death would miss them (the misclassification bug this fixes).
	// The captured group (.+?) holds the scope/model phrase between "your" and
	// "limit" ("session", "weekly Fable", "Fable"); bannerModelScoped classifies it.
	limitBannerRe = regexp.MustCompile(`(?i)you['’]?ve hit your (.+?) limit`)
	// limitScopeStripRe removes the ACCOUNT-scope qualifier tokens from a banner's
	// captured phrase; any non-empty remainder is a specific model name.
	limitScopeStripRe = regexp.MustCompile(`(?i)\b(session|weekly|usage|5[- ]hour)\b`)
	// limitPhraseRe matches quota phrasings an AGENT might legitimately write when
	// reviewing rate-limit code ("usage limit reached", "429"). These signal a limit
	// ONLY when the spawn actually died — the guard that keeps a successful reviewer's
	// transcript from being misread as a limit death and discarding its work.
	limitPhraseRe = regexp.MustCompile(`(?i)(usage limit reached|rate limit(?:ed)?|too many requests|\b429\b)`)
	// resetEpochRe matches claude's machine-readable form: "…limit reached|<unix>".
	resetEpochRe = regexp.MustCompile(`\|\s*(\d{10,})`)
	// resetClockRe matches a wall-clock reset like "reset at 3pm" / "resets 15:30",
	// with an optional trailing timezone name in parentheses ("resets 3:40pm
	// (Asia/Hong_Kong)") — the form Claude Code's banner actually uses.
	resetClockRe = regexp.MustCompile(`(?i)reset[s]?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^)]+)\))?`)
)

// bannerNetMax bounds how long a final result text may be and still be read, on an
// apparently-clean exit, as a bare harness banner rather than a fragment a
// SUCCESSFUL agent quoted. It is a defensive net for the (rare) case where a real
// limit stop looks clean AND carries no error subtype: the banner IS essentially
// the whole short message. A review/verdict quoting the banner is far longer and so
// excluded. It applies ONLY to the harness-exclusive limit banner, never to the
// generic transport phrases (which occur in ordinary successful prose).
const bannerNetMax = 240

// matchesDeath reports whether re matches a GENUINE terminal death signal in out.
// It fires only on a FAILED terminal (out.terminalFailed) — a successful spawn is
// never a death, no matter what its result OR its stderr contains (a run that logs a
// transient "529, retrying" to stderr but then exits 0 with a success subtype has
// succeeded, not died). On a failed terminal it scans the trustworthy channels: the
// process's stderr and start/run error text, plus the final result text.
//
// It deliberately never scans out.allText (the full joined transcript). This is the
// shared guard both classifiers use so neither can be tripped by an agent that
// merely discusses a limit/outage in a result — or a stderr line — of a spawn that
// actually completed.
func matchesDeath(out attemptOutcome, re *regexp.Regexp) bool {
	if !out.terminalFailed() {
		return false
	}
	hay := out.stderr + "\n" + out.lastText
	if out.startErr != nil {
		hay += "\n" + out.startErr.Error()
	}
	if out.runErr != nil {
		hay += "\n" + out.runErr.Error()
	}
	return re.MatchString(hay)
}

// classifyUsageLimit reports whether an attempt hit the account's usage limit, and
// when the limit resets. now is injected for tests. When it is a limit but no reset
// time is parseable it defaults to now + defaultLimitBackoff — a limit with an
// unknown reset still pauses, never fails.
//
// A limit is signalled by the harness's own banner (limitBannerRe) or the ambiguous
// quota phrases (limitPhraseRe). Both are gated by matchesDeath so a SUCCESSFUL spawn
// that merely mentions a limit is never misread. The banner additionally has a
// bounded defensive net: a bare short final text that IS the banner counts even on
// an apparently-clean exit, since a real limit stop can look clean while a verdict
// quoting the banner is far longer.
//
// Known residual (accepted): a <=bannerNetMax successful result that verbatim-quotes
// the harness banner would be read as a limit. The banner phrasing is harness-
// exclusive and a substantive verdict exceeds the bound, so this is far rarer than
// the missed-limit failure the net guards against.
func classifyUsageLimit(out attemptOutcome, now time.Time) (resetAt time.Time, modelScoped bool, ok bool) {
	last := strings.TrimSpace(out.lastText)
	bannerNet := len(last) <= bannerNetMax && limitBannerRe.MatchString(last)
	bannerDeath := matchesDeath(out, limitBannerRe)
	isLimit := bannerDeath || bannerNet || matchesDeath(out, limitPhraseRe)
	if !isLimit {
		return time.Time{}, false, false
	}
	// modelScoped ONLY when the banner regex fired (a death OR the short-clean-exit
	// net) AND its captured phrase names a specific model. The generic phrase path
	// (429 / rate limit / usage limit reached) is always account-scoped.
	if bannerDeath || bannerNet {
		hayB := out.stderr + "\n" + out.lastText
		if out.startErr != nil {
			hayB += "\n" + out.startErr.Error()
		}
		if out.runErr != nil {
			hayB += "\n" + out.runErr.Error()
		}
		if m := limitBannerRe.FindStringSubmatch(hayB); m != nil {
			modelScoped = bannerModelScoped(m[1])
		}
	}
	hay := out.stderr + "\n" + out.lastText
	if reset, ok := parseResetTime(hay, now); ok {
		return reset, modelScoped, true
	}
	return now.Add(defaultLimitBackoff), modelScoped, true
}

// bannerModelScoped classifies a banner's captured scope phrase as MODEL-scoped
// (a specific model like "Fable" / "weekly Opus") vs ACCOUNT-scoped ("session",
// "weekly", "usage", "5-hour"). It lowercases, strips the account qualifier tokens
// and collapses whitespace; a non-empty remainder is the model-name hint and means
// model scope. An empty remainder is account scope.
func bannerModelScoped(phrase string) bool {
	s := limitScopeStripRe.ReplaceAllString(strings.ToLower(phrase), " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " ")) != ""
}

// parseResetTime extracts the limit's reset moment from a claude limit message,
// relative to now. It handles the machine-readable epoch form ("|1700000000") and
// a wall-clock time ("reset at 3pm", "resets 15:30", "resets 3:40pm (Asia/Hong_Kong)");
// a named timezone in parentheses is honored via time.LoadLocation, and a wall-clock
// time already past (in that zone) rolls to tomorrow. ok=false when nothing parseable.
func parseResetTime(s string, now time.Time) (time.Time, bool) {
	if m := resetEpochRe.FindStringSubmatch(s); m != nil {
		if secs, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(secs, 0).In(now.Location()), true
		}
	}
	m := resetClockRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour > 23 {
		return time.Time{}, false
	}
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	switch strings.ToLower(m[3]) {
	case "pm":
		if hour < 12 {
			hour += 12
		}
	case "am":
		if hour == 12 {
			hour = 0
		}
	}
	if minute > 59 {
		return time.Time{}, false
	}
	// A named timezone in the banner ("(Asia/Hong_Kong)") is the wall clock's zone;
	// interpret the reset in it. An unknown/absent zone falls back to now's location.
	loc := now.Location()
	if m[4] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[4])); err == nil {
			loc = l
		}
	}
	base := now.In(loc)
	reset := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
	if !reset.After(now) {
		reset = reset.Add(24 * time.Hour) // the clock time already passed today
	}
	return reset, true
}

// --- connection / infrastructure death classification -------------------------

// infraBackoffs is the escalating wait between connection-loss retries. Unlike a
// usage limit (which carries its own reset time), an infra death has no schedule,
// so retries back off 1m → 5m → 15m → 1h and hold at 1h. Env-shrinkable for tests.
var infraBackoffs = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// infraGateThreshold is the number of CONSECUTIVE infra deaths after which the
// conductor-wide gate arms (a sustained outage is fleet-wide, like a limit). A
// single sporadic failure pauses only its own run and retries in place.
const infraGateThreshold = 2

// infraDeathRe matches the terminal signals of a network / infrastructure death —
// the class distinct from a usage limit and from an agent's own bad output. These
// are transient (a dropped connection, an auth flap during an outage, a 5xx), so a
// match pauses+retries with backoff instead of being misread as "produced no
// verdict". Kept to concrete network/transport signals so a genuine task failure or
// a deterministic crash (surfaced honestly) is NOT swallowed into an endless retry.
var infraDeathRe = regexp.MustCompile(`(?i)(unable to connect to api|connection ?refused|connection reset|econnrefused|econnreset|etimedout|i/o timeout|tls handshake timeout|failed to authenticate|api error: 401|api error: 5\d\d|\b529\b|overloaded_error|fetch failed|network is unreachable|temporary failure in name resolution|no such host|unexpected eof)`)

// classifyInfra reports whether an attempt died from a network/infrastructure
// fault. Via matchesDeath the transport signal counts only from a genuinely FAILED
// terminal — the process's stderr/error text, or the result text of a spawn that
// did not succeed — never a token a SUCCESSFUL spawn quoted inside a completed
// result (a reviewer auditing this repo's own networking code, a coder noting "the
// 529 backoff is handled" — either would otherwise be misread and retried forever).
// It also requires an explicit transport signal, not merely a non-zero exit, so a
// bare crash with no network evidence surfaces honestly rather than looping.
func classifyInfra(out attemptOutcome) bool {
	return matchesDeath(out, infraDeathRe)
}

// infraBackoff returns the wait before the streak-th consecutive infra retry
// (1-based), holding at the final step. Env override CANDYLAND_INFRA_BACKOFF_MS
// collapses every step to one small duration so tests don't wait minutes.
func infraBackoff(streak int) time.Duration {
	if v := envDur("CANDYLAND_INFRA_BACKOFF_MS", 0); v > 0 {
		return v
	}
	if streak < 1 {
		streak = 1
	}
	if streak > len(infraBackoffs) {
		streak = len(infraBackoffs)
	}
	return infraBackoffs[streak-1]
}

// --- conductor-wide limit gate ------------------------------------------------

// --- per-model gate (model-scoped fallback) -----------------------------------
//
// A MODEL-specific usage-limit banner ("You've hit your Fable limit") gates only
// that model, not the whole fleet: the interrupted spawn falls back to defaultModel
// (opus) and resumes immediately, and every later spawn of the gated model uses opus
// until its reset. Unlike the conductor-wide gate this is in-memory ONLY — never
// persisted; a restart's worst case is one re-death on the gated model. Guarded by
// the SAME limitMu as the conductor-wide gate (one lock, never nested with mu).

// armModelLimit gates one model until resetAt, never pulling the window earlier
// (monotonic, mirroring reArmLimit). Lazy-inits the map under the lock.
func (c *Conductor) armModelLimit(model string, resetAt time.Time) {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	if c.modelLimitUntil == nil {
		c.modelLimitUntil = map[string]time.Time{}
	}
	if resetAt.After(c.modelLimitUntil[model]) {
		c.modelLimitUntil[model] = resetAt
	}
}

// modelGated reports whether model's per-model window is still open at now.
func (c *Conductor) modelGated(model string, now time.Time) bool {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	return c.modelLimitUntil[model].After(now)
}

// effectiveModel maps a requested model to the model a spawn should actually run
// on: the requested model normally, or defaultModel while the requested model is
// gated. An empty request is defaultModel. A gated defaultModel is returned
// unchanged (nothing to fall back to) — the retry loop detects that un-fallible
// case and arms the fleet gate instead.
func (c *Conductor) effectiveModel(requested string, now time.Time) string {
	if requested == "" {
		return defaultModel
	}
	if c.modelGated(requested, now) {
		return defaultModel
	}
	return requested
}

// limitDeadline reads the current limit window's end (zero when no limit is armed).
func (c *Conductor) limitDeadline() time.Time {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	return c.limitUntil
}

// armLimit opens the conductor-wide gate until resetAt (never pulling it earlier)
// and, for a run host, persists the paused status + resumeAt so the dashboard shows
// the wait and a restart can re-arm the gate from storage. Quest hosts
// only move the gate — their status lifecycle is owned elsewhere.
func (c *Conductor) armLimit(hostID string, resetAt time.Time) {
	c.reArmLimit(resetAt)
	c.pausePersist(hostID, "usage limit — auto-resume at "+resetAt.UTC().Format(time.RFC3339), resetAt)
}

// armInfra arms the conductor-wide gate for a sustained (consecutive) connection
// loss and persists the paused status — the fleet-wide half, used once an outage
// looks systemic (>= infraGateThreshold consecutive infra deaths).
func (c *Conductor) armInfra(hostID string, resumeAt time.Time) {
	c.reArmLimit(resumeAt)
	c.pausePersist(hostID, "connection lost — retrying", resumeAt)
}

// pauseInfraLocal persists a connection-loss pause for a SINGLE sporadic failure
// WITHOUT arming the conductor-wide gate — only this run waits and retries; the
// rest of the fleet keeps running (a one-off blip is not an outage).
func (c *Conductor) pauseInfraLocal(hostID string, resumeAt time.Time) {
	c.pausePersist(hostID, "connection lost — retrying", resumeAt)
}

// pausePersist records the non-terminal auto-pause on a run host: status paused,
// the resumeAt marker, an incremented RePauses counter, and the human PauseReason
// the UI reads. Quest hosts only move the gate — their status lifecycle is
// owned elsewhere — so this is a no-op for them.
func (c *Conductor) pausePersist(hostID, reason string, resumeAt time.Time) {
	if !isRunID(hostID) {
		return
	}
	c.Update(hostID, func(r *run.Run) {
		r.Status = "paused"
		r.ResumeAt = resumeAt.UTC().Format(time.RFC3339)
		r.RePauses++ // telemetry: each genuine auto-pause; rehydrate (reArmLimit) never lands here
		r.PauseReason = reason
		r.StatusLine = reason
	})
}

// reArmLimit moves the gate no earlier than resetAt. It is the storage-restore
// entry point (rehydrate) as well as armLimit's gate half.
func (c *Conductor) reArmLimit(resetAt time.Time) {
	c.limitMu.Lock()
	if resetAt.After(c.limitUntil) {
		c.limitUntil = resetAt
	}
	c.limitMu.Unlock()
}

// resumeFromLimit flips a run host back to running once its interrupted spawn is
// about to resume (the gate has opened), clearing the persisted resumeAt.
func (c *Conductor) resumeFromLimit(hostID string) {
	if !isRunID(hostID) {
		return
	}
	c.Update(hostID, func(r *run.Run) {
		if r.Status == "paused" {
			r.Status = "running"
		}
		r.ResumeAt = ""
		r.PauseReason = ""
		r.StatusLine = ""
	})
}

// awaitLimit blocks until the armed limit window passes or ctx is cancelled. The
// gate can be pushed further out while waiting (a re-pause), so it re-reads the
// deadline each loop. Returns immediately when no limit is armed.
func (c *Conductor) awaitLimit(ctx context.Context) {
	for {
		d := time.Until(c.limitDeadline())
		if d <= 0 {
			return
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// isRunID reports whether an ooo host id is a run (r<N>), as opposed to a quest
// (q<N>). Runs own the paused/resumeAt status the limit gate persists; the quest
// host only moves the gate.
func isRunID(id string) bool {
	return !strings.HasPrefix(id, "q")
}
