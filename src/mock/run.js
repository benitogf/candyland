// UI config + helpers shared across the app. Run DATA is no longer here — it
// comes live from the ooo backend (see src/data/ooo.js). This module only holds
// presentation config (phases, state metadata, modes, workspaces) and small
// pure helpers that operate on a run object.
import { candy } from '../config'

export const PHASES = ['Plan', 'Build', 'Integrate', 'Review', 'PR']

// One worker's lifecycle state. `phase` buckets the rainbow into the only
// distinction that matters at a glance: in progress vs complete.
export const STATE_META = {
    idle: { label: 'Queued', color: 'text.secondary', dot: '#6b5c8a', phase: 'progress' },
    working: { label: 'Working', color: 'info.main', dot: candy.sky, phase: 'progress' },
    blocked: { label: 'Blocked', color: 'warning.main', dot: candy.lemon, phase: 'progress' },
    integrating: { label: 'Integrating', color: 'secondary.main', dot: candy.mint, phase: 'progress' },
    green: { label: 'Green', color: 'success.main', dot: '#7bdc6a', phase: 'done' },
    done: { label: 'Done', color: 'primary.main', dot: candy.pink, phase: 'done' },
}

export const isDone = (state) => STATE_META[state]?.phase === 'done'

// Developer vs non-developer changes the intake AND the whole UI: developer
// sees full live detail; non-developer sees a simplified progress view.
export const MODES = {
    developer: { label: 'Developer', tagline: 'open Q&A · full live detail — agents, tasks, raw logs', accent: candy.sky },
    'non-developer': { label: 'Non-developer', tagline: 'multiple-choice · a simple progress view, we handle the rest', accent: candy.pink },
}

// A workspace is a named set of folders. (Client config for now; the backend
// will serve these once workspace persistence lands.)
export const WORKSPACES = [
    { id: 'web', label: 'Web app', folders: ['~/src/acme/web', '~/src/acme/ui-kit'] },
    { id: 'reports-api', label: 'Reports API', folders: ['~/src/acme/reports-api', '~/src/acme/shared-go'] },
    { id: 'full-stack', label: 'Full stack (web + api)', folders: ['~/src/acme/web', '~/src/acme/ui-kit', '~/src/acme/reports-api'] },
    { id: 'mobile', label: 'Mobile app', folders: ['~/src/acme/mobile', '~/src/acme/ui-kit'] },
    { id: 'infra', label: 'Infra & deploy', folders: ['~/src/acme/infra', '~/src/acme/terraform', '~/src/acme/ci'] },
    { id: 'docs', label: 'Docs site', folders: ['~/src/acme/docs'] },
]

export const workspaceById = (id) => WORKSPACES.find((w) => w.id === id) || null

// Find an agent within a run object (run comes from live ooo state).
export const agentInRun = (run, id) => (run ? (run.agents || []).find((a) => a.id === id) || null : null)
