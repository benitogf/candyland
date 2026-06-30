import { domain, ssl } from '../config'

// REST calls to the conductor. Candyland is observe-only: runs are CREATED and
// STARTED by detritus over REST (POST /api/runs → /api/runs/{id}/begin). The UI
// only observes and controls EXISTING runs (stop / restart / cancel / archive /
// edit) — it never creates or plans a run.
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

// Stop | restart.
export const commandRun = (id, command) => post(`/runs/${id}/command`, { command })

// Cancel: abandon a run (works while still in the planning Q&A, where stop has no
// executor to reach). The run is kept as "cancelled" in the Tasks history.
export const cancelRun = (id) => post(`/runs/${id}/cancel`)

// Archive: clear a run from the dashboard, keeping it in the Tasks history.
export const archiveRun = (id) => post(`/runs/${id}/archive`)

// Edit: change a finished run's task in place and reset it to planning, then it
// re-runs. Distinct from restart (re-run as-is).
export const editRun = (id, spec) => post(`/runs/${id}/edit`, spec)

// System info: platform, dependency state (claude/git/gh), recommendations.
// Doubles as the backend reachability probe.
export const fetchSystem = async () => {
    const res = await fetch(`${base}/system`)
    if (!res.ok) throw new Error(`system: ${res.status}`)
    return res.json()
}
