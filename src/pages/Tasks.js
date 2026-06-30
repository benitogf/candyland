import React, { useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import Link from '@mui/material/Link'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import Typography from '@mui/material/Typography'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'

import { PHASES, STATUS_COLOR } from '../meta/run'
import { runLabel } from '../util'
import { useRuns, useQuests, useCampaigns } from '../data/ooo'
import { readFilters, matchFilters, folderOf } from '../data/filters'
import FilterBar from '../components/FilterBar'

// ── The one work/history section ─────────────────────────────────────────────
// A single section that PIVOTS by level — Runs/Tasks · Quests · Campaigns —
// without navigating to a different top-level page. The pivot and the shared
// filters live in the URL query string, so pivoting between levels (or following
// a parent/child link) keeps the active filters. There is no separate "Quests"
// or "Campaigns" nav item; this is the whole work history, one section.

const LEVELS = [
    { key: 'runs', label: 'Runs / Tasks' },
    { key: 'quests', label: 'Quests' },
    { key: 'campaigns', label: 'Campaigns' },
]

const statusText = (r) => {
    if (r.status === 'running' && typeof r.phase === 'number') return PHASES[r.phase] || 'Running'
    return r.status ? r.status.charAt(0).toUpperCase() + r.status.slice(1) : '—'
}

const StatusChip = ({ status, text }) => (
    <Chip size="small" variant="outlined" color={STATUS_COLOR[status] || 'default'} label={text} sx={{ height: 22 }} />
)

const PrCell = ({ url, count }) => (
    <TableCell onClick={(e) => e.stopPropagation()}>
        {url
            ? <Link href={url} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>PR <OpenInNewIcon sx={{ fontSize: 13 }} /></Link>
            : count
                ? <Typography variant="caption" color="text.secondary">{count} PR{count > 1 ? 's' : ''}</Typography>
                : <Typography variant="caption" color="text.secondary">—</Typography>}
    </TableCell>
)

// A clickable parent link that pivots the section to the parent level, filtered
// to that parent — keeping the rest of the current filters intact.
const ParentLink = ({ id, level, onPivot }) => (
    <Link
        component="button"
        type="button"
        onClick={(e) => { e.stopPropagation(); onPivot(level, id) }}
        sx={{ fontFamily: 'monospace', fontSize: 12 }}
    >
        {id}
    </Link>
)

const FolderText = ({ folder }) => (
    <Typography
        variant="body2" component="span" title={folder}
        sx={{ color: 'text.secondary', fontFamily: 'monospace', fontSize: 12, maxWidth: 240, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', verticalAlign: 'bottom' }}
    >
        {folder || '—'}
    </Typography>
)

// ── Per-level row + header definitions. Each returns the header cells and a row
//    renderer so the table body is shared. ─────────────────────────────────────

const RunsTable = ({ rows, onOpen, onPivot }) => (
    <>
        <TableHead>
            <TableRow>
                <TableCell sx={{ fontWeight: 700 }}>Task</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Status</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Parent</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Folder</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>PR</TableCell>
            </TableRow>
        </TableHead>
        <TableBody>
            {rows.map((r) => (
                <TableRow key={r.id} hover onClick={() => onOpen('run', r.id)} sx={{ cursor: 'pointer', opacity: r.archived ? 0.6 : 1 }}>
                    <TableCell>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                            <Typography variant="body2" sx={{ fontWeight: 600 }}>{runLabel(r)}</Typography>
                            {r.archived && <Chip size="small" variant="outlined" label="cleared" sx={{ height: 18, fontSize: 10 }} />}
                        </Box>
                    </TableCell>
                    <TableCell><StatusChip status={r.status} text={statusText(r)} /></TableCell>
                    <TableCell>
                        {r.campaignId && <ParentLink id={r.campaignId} level="campaigns" onPivot={onPivot} />}
                        {r.campaignId && r.questId && ' · '}
                        {r.questId && <ParentLink id={r.questId} level="quests" onPivot={onPivot} />}
                        {!r.campaignId && !r.questId && <Typography variant="caption" color="text.secondary">—</Typography>}
                    </TableCell>
                    <TableCell><FolderText folder={folderOf(r)} /></TableCell>
                    <PrCell url={r.prUrl} />
                </TableRow>
            ))}
        </TableBody>
    </>
)

const QuestsTable = ({ rows, onOpen, onPivot }) => (
    <>
        <TableHead>
            <TableRow>
                <TableCell sx={{ fontWeight: 700 }}>Objective</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Status</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Campaign</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Progress</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>PRs</TableCell>
            </TableRow>
        </TableHead>
        <TableBody>
            {rows.map((q) => (
                <TableRow key={q.id} hover onClick={() => onOpen('quest', q.id)} sx={{ cursor: 'pointer' }}>
                    <TableCell>
                        <Typography variant="body2" sx={{ fontWeight: 600 }}>{q.objective || q.originalObjective || q.id}</Typography>
                        <FolderText folder={folderOf(q)} />
                    </TableCell>
                    <TableCell><StatusChip status={q.status} text={statusText(q)} /></TableCell>
                    <TableCell>
                        {q.campaignId
                            ? <ParentLink id={q.campaignId} level="campaigns" onPivot={onPivot} />
                            : <Typography variant="caption" color="text.secondary">standalone</Typography>}
                    </TableCell>
                    <TableCell>
                        <Typography variant="caption" color="text.secondary">{q.itemsCompleted || 0} done · {q.itemsBlocked || 0} blocked</Typography>
                    </TableCell>
                    <PrCell count={q.prsOpened || 0} />
                </TableRow>
            ))}
        </TableBody>
    </>
)

const CampaignsTable = ({ rows, onOpen }) => (
    <>
        <TableHead>
            <TableRow>
                <TableCell sx={{ fontWeight: 700 }}>Intent</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Status</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Children</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>PRs</TableCell>
            </TableRow>
        </TableHead>
        <TableBody>
            {rows.map((c) => (
                <TableRow key={c.id} hover onClick={() => onOpen('campaign', c.id)} sx={{ cursor: 'pointer' }}>
                    <TableCell>
                        <Typography variant="body2" sx={{ fontWeight: 600 }}>{c.intentBrief?.restatedGoal || c.originalInput || c.id}</Typography>
                    </TableCell>
                    <TableCell><StatusChip status={c.status} text={statusText(c)} /></TableCell>
                    <TableCell>
                        <Typography variant="caption" color="text.secondary">{(c.questIds || []).length} quests · {(c.runIds || []).length} runs</Typography>
                    </TableCell>
                    <PrCell count={(c.prs || []).length} />
                </TableRow>
            ))}
        </TableBody>
    </>
)

const COLSPAN = { runs: 5, quests: 5, campaigns: 4 }

// Text fields each level is searched over.
const textFieldsFor = (item, level) => {
    if (level === 'runs') return [runLabel(item), item.status, folderOf(item), item.prompt, item.branch, item.id]
    if (level === 'quests') return [item.objective, item.originalObjective, item.status, folderOf(item), item.id]
    return [item.originalInput, item.intentBrief?.restatedGoal, item.status, item.id]
}

const Tasks = () => {
    const navigate = useNavigate()
    const [params, setParams] = useSearchParams()
    const level = LEVELS.some((l) => l.key === params.get('level')) ? params.get('level') : 'runs'
    const filters = readFilters(params)

    const runs = useRuns()
    const quests = useQuests()
    const campaigns = useCampaigns()
    const items = level === 'runs' ? runs : level === 'quests' ? quests : campaigns

    const filtered = useMemo(
        () => items.filter((it) => matchFilters(it, filters, level, textFieldsFor(it, level))),
        [items, filters, level],
    )

    // Pivot/filter mutations all go through the URL so links preserve filters.
    const setLevel = (next) => {
        if (!next) return
        const p = new URLSearchParams(params)
        p.set('level', next)
        setParams(p, { replace: true })
    }
    // Pivot to a parent/child level filtered to a specific parent id, keeping the
    // rest of the active filters.
    const pivotToParent = (nextLevel, parentId) => {
        const p = new URLSearchParams(params)
        p.set('level', nextLevel)
        if (parentId) p.set('parent', parentId)
        else p.delete('parent')
        setParams(p)
    }
    const setFilter = (key, value) => {
        const p = new URLSearchParams(params)
        if (value) p.set(key, value)
        else p.delete(key)
        setParams(p, { replace: true })
    }
    const clearFilters = () => {
        const p = new URLSearchParams()
        p.set('level', level)
        setParams(p, { replace: true })
    }

    const openDetail = (kind, id) => navigate(`/${kind}/${id}`)

    const empty = items.length === 0
        ? `No ${level} yet — they're launched from detritus.`
        : 'Nothing matches the active filters.'

    return (
        <Box>
            <Typography variant="h5" sx={{ fontWeight: 800 }}>Work</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                Every run, quest, and campaign in any state. Pivot the level; filters carry across.
            </Typography>

            <ToggleButtonGroup
                size="small" exclusive value={level} onChange={(_, v) => setLevel(v)} sx={{ mb: 2 }} aria-label="work level"
            >
                {LEVELS.map((l) => <ToggleButton key={l.key} value={l.key}>{l.label}</ToggleButton>)}
            </ToggleButtonGroup>

            <FilterBar
                level={level}
                filters={filters}
                runs={runs}
                quests={quests}
                campaigns={campaigns}
                onChange={setFilter}
                onClear={clearFilters}
            />

            <Card sx={{ overflowX: 'auto' }}>
                <Table size="small" sx={{ minWidth: 720 }}>
                    {filtered.length === 0
                        ? <TableBody><TableRow><TableCell colSpan={COLSPAN[level]} sx={{ color: 'text.secondary' }}>{empty}</TableCell></TableRow></TableBody>
                        : level === 'runs' ? <RunsTable rows={filtered} onOpen={openDetail} onPivot={pivotToParent} />
                            : level === 'quests' ? <QuestsTable rows={filtered} onOpen={openDetail} onPivot={pivotToParent} />
                                : <CampaignsTable rows={filtered} onOpen={openDetail} />}
                </Table>
            </Card>
        </Box>
    )
}

export default Tasks
