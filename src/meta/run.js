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
}

export const isDone = (state) => STATE_META[state]?.phase === 'done'

// Find an agent within a run object (run comes from live ooo state).
export const agentInRun = (run, id) => (run ? (run.agents || []).find((a) => a.id === id) || null : null)

// Status → MUI color, shared by the work/history section across all three levels
// (runs, quests, campaigns). Quests/campaigns add running|paused|stopped|blocked
// to the run statuses; a missing entry falls back to 'default'.
export const STATUS_COLOR = {
    done: 'success',
    completed: 'success',
    cancelled: 'default',
    stopped: 'default',
    paused: 'warning',
    blocked: 'warning',
    running: 'info',
    planning: 'secondary',
}

// Autonomy level → short human label (L1 report-only | L2 gate-PR | L3 unattended).
export const AUTONOMY_LABEL = {
    L1: 'L1 · report-only',
    L2: 'L2 · gate PRs',
    L3: 'L3 · unattended',
}
