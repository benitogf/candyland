import { domain, ssl } from '../config'

// REST calls to the conductor. Runs are CREATED and STARTED by detritus over
// REST (POST /api/runs → /api/runs/{id}/begin). The UI observes work, STOPS it,
// and CONFIGURES the conductor (per-level model/thinking via the settings
// endpoint) — Stop is the single interaction on runs and quests (no
// restart, edit, pause, or resume). It never creates or plans a run.
const base = `${ssl ? 'https' : 'http'}://${domain}/api`

const post = async (path, body) => {
    const res = await fetch(base + path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: body ? JSON.stringify(body) : undefined,
    })
    if (!res.ok) {
        // Surface the server's reason (e.g. "folder is not readable + writable: …")
        // so the UI can tell the user what to fix, not just a status code.
        const detail = (await res.text().catch(() => '')).trim()
        throw new Error(detail || `${path}: ${res.status}`)
    }
    const text = await res.text()
    return text ? JSON.parse(text) : null
}

// Stop: the only run interaction. Terminal and irreversible.
export const stopRun = (id) => post(`/runs/${id}/command`, { command: 'stop' })

// Cancel: abandon a run (works while still in the planning Q&A, where stop has no
// executor to reach). The run is kept as "cancelled" in the Tasks history.
export const cancelRun = (id) => post(`/runs/${id}/cancel`)

// Archive (dismiss): clear a finished/blocked/stopped item from the dashboard. It
// stays in the Work history — archive hides, it never deletes. Available for runs
// and quests.
export const archiveRun = (id) => post(`/runs/${id}/archive`)
export const archiveQuest = (id) => post(`/quests/${id}/archive`)

// Quest control. Stop is the only control the backend exposes — terminal,
// irreversible, and it CASCADES to children (stopping a quest stops its runs).
// Stop carries an optional reason recorded on the record.
export const stopQuest = (id, reason) => post(`/quests/${id}/stop`, reason ? { reason } : undefined)

// Settings: the per-level agent config (model + thinking per role). GET returns
// the full defaults-overlaid object `{levels:{<role>:{model,thinking}}}`; POST
// validates + saves the whole object and returns the saved result. This is the
// UI's one configuration surface — it still never creates or plans work.
export const fetchSettings = async () => {
    const res = await fetch(`${base}/settings`)
    if (!res.ok) throw new Error(`settings: ${res.status}`)
    return res.json()
}
export const saveSettings = (levels) => post('/settings', { levels })

// System info: platform, dependency state (claude/git/gh), recommendations.
// Doubles as the backend reachability probe.
export const fetchSystem = async () => {
    const res = await fetch(`${base}/system`)
    if (!res.ok) throw new Error(`system: ${res.status}`)
    return res.json()
}
