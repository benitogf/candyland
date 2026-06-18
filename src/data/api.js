import { domain, ssl } from '../config'

// REST calls to the conductor. Creation, begin-after-planning, run commands,
// and planning questions all come from the backend — nothing is hardcoded here.
const base = `${ssl ? 'https' : 'http'}://${domain}/api`

const post = async (path, body) => {
    const res = await fetch(base + path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: body ? JSON.stringify(body) : undefined,
    })
    if (!res.ok) throw new Error(`${path}: ${res.status}`)
    const text = await res.text()
    return text ? JSON.parse(text) : null
}

// Create a run from the wizard; returns { id }.
export const createRun = (spec) => post('/runs', spec)

// Begin the build once planning is done; the planning answers refine the prompt.
export const beginRun = (id, answers) => post(`/runs/${id}/begin`, { answers })

// Stop | restart.
export const commandRun = (id, command) => post(`/runs/${id}/command`, { command })

// System info: platform, dependency state, executor mode, recommendations.
// Doubles as the backend reachability probe.
export const fetchSystem = async () => {
    const res = await fetch(`${base}/system`)
    if (!res.ok) throw new Error(`system: ${res.status}`)
    return res.json()
}

// Workspace CRUD (named folder sets).
export const createWorkspace = (ws) => post('/workspaces', ws)
export const deleteWorkspace = async (id) => {
    const res = await fetch(`${base}/workspaces/${id}`, { method: 'DELETE' })
    if (!res.ok) throw new Error(`delete workspace: ${res.status}`)
}

// Planning questions for a mode (served by the backend).
export const fetchQuestions = async (mode) => {
    const res = await fetch(`${base}/questions?mode=${encodeURIComponent(mode)}`)
    if (!res.ok) throw new Error(`questions: ${res.status}`)
    return res.json()
}
