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

// Stop | resume | restart.
export const commandRun = (id, command) => post(`/runs/${id}/command`, { command })

// Workspace CRUD (named folder sets).
export const createWorkspace = (ws) => post('/workspaces', ws)
export const deleteWorkspace = (id) => fetch(`${base}/workspaces/${id}`, { method: 'DELETE' })

// Planning questions for a mode (served by the backend).
export const fetchQuestions = async (mode) => {
    const res = await fetch(`${base}/questions?mode=${encodeURIComponent(mode)}`)
    if (!res.ok) throw new Error(`questions: ${res.status}`)
    return res.json()
}
