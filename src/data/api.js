import { domain, ssl } from '../config'

// REST calls to the conductor. Candyland is observe-only: runs are CREATED and
// STARTED by detritus over REST (POST /api/runs → /api/runs/{id}/begin). The UI
// only observes and STOPS existing work — Stop is the single interaction on runs,
// quests, and campaigns (no restart, edit, pause, or resume). It never creates or
// plans a run.
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

// Quest / campaign control. Stop is the only control the backend exposes for
// either — terminal, irreversible, and it CASCADES to children (stopping a
// campaign stops its quests and their runs; stopping a quest stops its runs).
// Stop carries an optional reason recorded on the record.
export const stopQuest = (id, reason) => post(`/quests/${id}/stop`, reason ? { reason } : undefined)
export const stopCampaign = (id, reason) => post(`/campaigns/${id}/stop`, reason ? { reason } : undefined)

// System info: platform, dependency state (claude/git/gh), recommendations.
// Doubles as the backend reachability probe.
export const fetchSystem = async () => {
    const res = await fetch(`${base}/system`)
    if (!res.ok) throw new Error(`system: ${res.status}`)
    return res.json()
}
