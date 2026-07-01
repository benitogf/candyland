// Rollup: the substance of the quest/campaign detail views. Instead of restating
// intent, these helpers aggregate a parent's children into the numbers that say
// how far the work has actually gotten — progress across children, per-repo
// delivery state, aggregate agent activity, review/verdict rollups, and timing.
// All computation is client-side over the already-served ooo state; nothing new
// is required from the backend beyond the fields the views already receive.
import React from 'react'
import Box from '@mui/material/Box'
import Chip from '@mui/material/Chip'
import Typography from '@mui/material/Typography'
import LinearProgress from '@mui/material/LinearProgress'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import Link from '@mui/material/Link'

// Terminal run/quest statuses count as "delivered/finished" for progress.
const DONE_STATUSES = new Set(['done', 'completed', 'stopped', 'cancelled', 'surfaced-only'])
export const isFinished = (status) => DONE_STATUSES.has(status)

// Short relative-ish timestamp: the date/time as the backend gave it (RFC3339),
// trimmed to minutes so the timing line stays readable.
export const shortTime = (ts) => {
    if (!ts) return '—'
    const d = new Date(ts)
    if (Number.isNaN(d.getTime())) return ts
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

// One headline number with a label under it.
export const Stat = ({ label, value, sub, color }) => (
    <Box sx={{ minWidth: 90 }}>
        <Typography variant="h5" sx={{ fontWeight: 800, color: color || 'text.primary', lineHeight: 1.1 }}>{value}</Typography>
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>{label}</Typography>
        {sub && <Typography variant="caption" color="text.secondary" sx={{ display: 'block', opacity: 0.8 }}>{sub}</Typography>}
    </Box>
)

// A row of stats with a progress bar above it (progress across children).
export const StatGrid = ({ done, total, children }) => {
    const pct = total > 0 ? Math.round((done / total) * 100) : 0
    return (
        <Box>
            {total > 0 && (
                <Box sx={{ mb: 2 }}>
                    <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 0.5 }}>
                        <Typography variant="caption" color="text.secondary">progress across children</Typography>
                        <Typography variant="caption" color="text.secondary">{done}/{total} finished · {pct}%</Typography>
                    </Box>
                    <LinearProgress variant="determinate" value={pct} color={pct === 100 ? 'success' : 'primary'} sx={{ height: 6, borderRadius: 3 }} />
                </Box>
            )}
            <Box sx={{ display: 'flex', gap: 3, flexWrap: 'wrap' }}>{children}</Box>
        </Box>
    )
}

// Per-repo delivery state: how many PRs are open / failed / pending per repo,
// merged from every PR record the parent (or its ticks) produced.
export const repoDelivery = (prs) => {
    const byRepo = new Map()
    for (const p of prs || []) {
        const key = p.repo || '(repo)'
        const r = byRepo.get(key) || { repo: key, open: 0, failed: 0, urls: [] }
        if (p.url) { r.open++; r.urls.push(p.url) } else { r.failed++ }
        byRepo.set(key, r)
    }
    return [...byRepo.values()]
}

export const RepoDelivery = ({ prs }) => {
    const repos = repoDelivery(prs)
    if (repos.length === 0) return <Typography variant="body2" color="text.secondary">No delivery yet.</Typography>
    return (
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
            {repos.map((r) => (
                <Box key={r.repo} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Typography variant="body2" sx={{ fontFamily: 'monospace', minWidth: 160 }}>{r.repo}</Typography>
                    {r.open > 0 && <Chip size="small" variant="outlined" color="success" label={`${r.open} PR${r.open > 1 ? 's' : ''} open`} sx={{ height: 20 }} />}
                    {r.failed > 0 && <Chip size="small" variant="outlined" color="error" label={`${r.failed} failed`} sx={{ height: 20 }} />}
                    {r.urls.map((u, i) => (
                        <Link key={i} href={u} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.3 }}>PR <OpenInNewIcon sx={{ fontSize: 12 }} /></Link>
                    ))}
                </Box>
            ))}
        </Box>
    )
}

// Aggregate agent activity across a set of agent-bearing entities (child runs,
// child quests, and the parent's own coordinating agents). Buckets agents by
// working vs done vs other, and sums tokens.
export const agentActivity = (entities) => {
    let working = 0; let done = 0; let other = 0; let tokens = 0; let total = 0
    for (const e of entities || []) {
        for (const a of e?.agents || []) {
            total++
            tokens += a.tokens || 0
            if (a.state === 'working' || a.state === 'retrying' || a.state === 'integrating') working++
            else if (a.state === 'green' || a.state === 'done') done++
            else other++
        }
    }
    return { working, done, other, tokens, total }
}

export const AgentActivity = ({ entities }) => {
    const a = agentActivity(entities)
    if (a.total === 0) return <Typography variant="body2" color="text.secondary">No agent activity yet.</Typography>
    return (
        <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', alignItems: 'center' }}>
            <Chip size="small" variant="outlined" label={`${a.total} agent${a.total > 1 ? 's' : ''}`} sx={{ height: 22 }} />
            {a.working > 0 && <Chip size="small" variant="outlined" color="info" label={`${a.working} working`} sx={{ height: 22 }} />}
            {a.done > 0 && <Chip size="small" variant="outlined" color="success" label={`${a.done} done`} sx={{ height: 22 }} />}
            {a.other > 0 && <Chip size="small" variant="outlined" label={`${a.other} idle/blocked`} sx={{ height: 22 }} />}
            <Typography variant="body2" color="text.secondary">· {a.tokens.toLocaleString()} tokens across children</Typography>
        </Box>
    )
}
