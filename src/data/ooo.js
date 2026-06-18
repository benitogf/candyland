import { useEffect, useState } from 'react'
import oooClient from 'ooo-client'

import { domain, ssl } from '../config'

// Live ooo subscriptions. Each hook opens a WebSocket to a realtime key and
// re-renders on every update — this is how the UI reads run state. No polling,
// no client-side mock; the conductor is the single source of truth.
const useOoo = (key) => {
    const [cache, setCache] = useState(null)
    useEffect(() => {
        if (!key) return undefined
        const conn = oooClient(`${domain}/${key}`, ssl)
        // ooo-client applies JSON-patches in place, so conn.cache keeps the same
        // reference across updates — clone so React sees a new value and re-renders.
        const update = () => setCache(conn.cache == null ? null : JSON.parse(JSON.stringify(conn.cache)))
        conn.onopen = update
        conn.onmessage = update
        return () => conn.close()
    }, [key])
    return cache
}

// All runs (for the dashboard), newest first. ooo lists ascending by key, so we
// sort by the run's sequence id (r1, r2, …) descending.
const seq = (r) => parseInt(String(r.id).replace(/\D/g, ''), 10) || 0
export const useRuns = () => {
    const cache = useOoo('runs/*')
    if (!Array.isArray(cache)) return []
    return cache.map((e) => e?.data).filter(Boolean).sort((a, b) => seq(b) - seq(a))
}

// One run (for the workspace), live.
export const useRun = (id) => {
    const cache = useOoo(id ? `runs/${encodeURIComponent(id)}` : null)
    return cache?.data || null
}

// Saved workspaces (named folder sets), live from the backend.
export const useWorkspaces = () => {
    const cache = useOoo('workspaces/*')
    if (!Array.isArray(cache)) return []
    return cache.map((e) => e?.data).filter(Boolean).sort((a, b) => (a.label || '').localeCompare(b.label || ''))
}

export const workspaceById = (list, id) => (list || []).find((w) => w.id === id) || null
