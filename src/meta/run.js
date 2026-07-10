// UI config + helpers shared across the app. Run DATA is no longer here — it
// comes live from the ooo backend (see src/data/ooo.js). This module only holds
// presentation config (phases, state metadata) and small pure helpers that
// operate on a run object.
import { candy } from '../config'

export const PHASES = ['Build', 'Integrate', 'Review', 'PR']

// One worker's lifecycle state. `phase` buckets the rainbow into the only
// distinction that matters at a glance: in progress vs complete.
export const STATE_META = {
    idle: { label: 'Queued', color: 'text.secondary', dot: '#6b5c8a', phase: 'progress' },
    working: { label: 'Working', color: 'info.main', dot: candy.sky, phase: 'progress' },
    retrying: { label: 'Retrying', color: 'warning.main', dot: '#ffa94d', phase: 'progress' },
    blocked: { label: 'Blocked', color: 'warning.main', dot: candy.lemon, phase: 'progress' },
    integrating: { label: 'Integrating', color: 'secondary.main', dot: candy.mint, phase: 'progress' },
    green: { label: 'Green', color: 'success.main', dot: '#7bdc6a', phase: 'done' },
    done: { label: 'Done', color: 'primary.main', dot: candy.pink, phase: 'done' },
    // A worker killed by a stop: terminal, but not a success. Rendered neutral so
    // a stopped run's agent cards read "Stopped", never a lingering "Working".
    stopped: { label: 'Stopped', color: 'text.secondary', dot: '#6b5c8a', phase: 'done' },
}

export const isDone = (state) => STATE_META[state]?.phase === 'done'

// ── Auto-pause presentation ──────────────────────────────────────────────────
// The conductor now AUTO-PAUSES work (and auto-resumes) instead of failing it on
// two distinct causes, and the UI must tell them apart — a transient usage/token
// limit (work resumes when the limit resets) vs a connection/infra death (work
// retries with backoff). Runs and quests all carry the same fields
// (status:'paused' + pauseReason + resumeAt + rePauses), so this is uniform.
export const PAUSE_META = {
    // Usage/token limit — a scheduled, time-bounded wait; reads with a clock.
    limit: { key: 'limit', label: 'Usage limit', color: 'warning' },
    // Connection/infra loss — an open-ended retry; reads as an outage, not a clock.
    connection: { key: 'connection', label: 'Connection lost', color: 'warning' },
    // Any other auto-pause the backend introduces — a safe, generic fallback.
    other: { key: 'other', label: 'Paused', color: 'warning' },
}

// Classify a pauseReason string into its cause. Keys off the two guaranteed
// forms ("usage limit — …" / "connection lost — …"), tolerant of wording drift.
export const pauseCause = (reason) => {
    const r = String(reason || '').toLowerCase()
    if (r.includes('usage limit') || r.includes('token')) return 'limit'
    if (r.includes('connection') || r.includes('network')) return 'connection'
    return 'other'
}

// Distill an entity's pause fields into a presentable shape, or null when it is
// not auto-paused. Callers render the cause differently per `meta`/`cause`.
export const pauseInfo = (entity) => {
    if (!entity || entity.status !== 'paused') return null
    const cause = pauseCause(entity.pauseReason)
    return {
        cause,
        meta: PAUSE_META[cause],
        reason: entity.pauseReason || PAUSE_META[cause].label,
        resumeAt: entity.resumeAt || '',
        rePauses: entity.rePauses || 0,
    }
}

// Human resume time: "3:40 PM". Empty when unset/invalid.
export const resumeAtLabel = (resumeAt) => {
    if (!resumeAt) return ''
    const d = new Date(resumeAt)
    if (Number.isNaN(d.getTime())) return ''
    return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

// Find an agent within a run object (run comes from live ooo state).
export const agentInRun = (run, id) => (run ? (run.agents || []).find((a) => a.id === id) || null : null)

// Status → MUI color, shared by the work/history section across both levels
// (runs, quests). Quests add running|paused|stopped|blocked to the run statuses;
// a missing entry falls back to 'default'.
export const STATUS_COLOR = {
    done: 'success',
    completed: 'success',
    // A no-op terminal — surfaced/skipped work but executed 0 and opened 0 PRs.
    // Distinct from done (success) and from error/blocked (warning): rendered
    // 'info' so it reads as an honest, informational terminal, not a success.
    'surfaced-only': 'info',
    cancelled: 'default',
    stopped: 'default',
    paused: 'warning',
    blocked: 'warning',
    running: 'info',
    planning: 'secondary',
}
