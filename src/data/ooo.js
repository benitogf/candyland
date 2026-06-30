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

// Normalize a run for the UI: agents/tasks are always arrays, so panels can
// .map/.length them safely no matter what the backend (or older persisted data)
// sent — a null there would otherwise crash the whole view.
const normalizeRun = (r) => (r ? { ...r, agents: r.agents || [], tasks: r.tasks || [] } : null)

// All runs (for the dashboard), newest first. ooo lists ascending by key, so we
// sort by the run's sequence id (r1, r2, …) descending.
const seq = (r) => parseInt(String(r.id).replace(/\D/g, ''), 10) || 0
export const useRuns = () => {
    const cache = useOoo('runs/*')
    if (!Array.isArray(cache)) return []
    return cache.map((e) => e?.data).filter(Boolean).map(normalizeRun).sort((a, b) => seq(b) - seq(a))
}

// One run, live.
export const useRun = (id) => {
    const cache = useOoo(id ? `runs/${encodeURIComponent(id)}` : null)
    return normalizeRun(cache?.data || null)
}

// One run's audit record (the verification timeline), live from audits/<id>.
// Null until the run reaches a terminal state and the conductor writes it.
export const useAudit = (id) => {
    const cache = useOoo(id ? `audits/${encodeURIComponent(id)}` : null)
    return cache?.data || null
}

// ── Quests & Campaigns ───────────────────────────────────────────────────────
// The work/history section pivots Runs ↔ Quests ↔ Campaigns over three open ooo
// filters (runs/*, quests/*, campaigns/*), all read live the same way as runs —
// the conductor is the single source of truth, no polling, no client-side mock.

// All quests, newest first by sequence id (q1, q2, …), mirroring useRuns.
export const useQuests = () => {
    const cache = useOoo('quests/*')
    if (!Array.isArray(cache)) return []
    return cache.map((e) => e?.data).filter(Boolean).sort((a, b) => seq(b) - seq(a))
}

// One quest, live.
export const useQuest = (id) => {
    const cache = useOoo(id ? `quests/${encodeURIComponent(id)}` : null)
    return cache?.data || null
}

// All campaigns, newest first by sequence id (c1, c2, …), mirroring useRuns.
export const useCampaigns = () => {
    const cache = useOoo('campaigns/*')
    if (!Array.isArray(cache)) return []
    return cache.map((e) => e?.data).filter(Boolean).sort((a, b) => seq(b) - seq(a))
}

// One campaign, live.
export const useCampaign = (id) => {
    const cache = useOoo(id ? `campaigns/${encodeURIComponent(id)}` : null)
    return cache?.data || null
}
