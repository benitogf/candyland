import React, { useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Link from '@mui/material/Link'
import Tooltip from '@mui/material/Tooltip'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import Typography from '@mui/material/Typography'
import OpenInFullIcon from '@mui/icons-material/OpenInFull'

import { PHASES, STATUS_COLOR } from '../meta/run'
import { runLabel, questLabel } from '../util'
import { useRuns, useQuests, deliverOf } from '../data/ooo'
import { readFilters, matchFilters, folderOf } from '../data/filters'
import FilterBar from '../components/FilterBar'
import CopyReference from '../components/CopyReference'
import { CopyPrLink } from '../components/CopyPr'
import { PauseChip } from '../components/StatusBits'

// ── The one work/history section ─────────────────────────────────────────────
// A single section that PIVOTS by level — Runs/Tasks · Quests · Adventures —
// without navigating to a different top-level page. The pivot and the shared
// filters live in the URL query string, so pivoting between levels (or following
// a parent/child link) keeps the active filters. There is no separate "Quests"
// nav item; this is the whole work history, one section.

const LEVELS = [
    { key: 'runs', label: 'Runs / Tasks' },
    { key: 'quests', label: 'Quests' },
    { key: 'adventures', label: 'Adventures' },
]

const statusText = (r) => {
    if (r.status === 'running' && typeof r.phase === 'number') return PHASES[r.phase] || 'Running'
    return r.status ? r.status.charAt(0).toUpperCase() + r.status.slice(1) : '—'
}

// Hard 2-line clamp for the Work-table title cells — a legacy title-less item
// can carry a huge objective; the full text stays in the detail views.
const clamp2 = { display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden', wordBreak: 'break-word', maxWidth: 480 }

// The status cell. For an auto-paused item, render the cause-distinguishing
// PauseChip (usage-limit vs connection-loss) in place of the generic chip; every
// other status keeps the plain colored chip.
const StatusChip = ({ item, status, text }) => (
    item && item.status === 'paused'
        ? <PauseChip entity={item} />
        : <Chip size="small" variant="outlined" color={STATUS_COLOR[status] || 'default'} label={text} sx={{ height: 22 }} />
)

// A per-row open-details button. Row clicks DRILL into children (pivot the
// section); this button OPENS the item's detail view instead, so it stops
// propagation to preserve the drill-on-row-click behaviour.
const OpenDetailButton = ({ kind, id, onOpen }) => (
    <Tooltip title="Open details">
        <IconButton
            size="small"
            aria-label={`open ${kind} details`}
            onClick={(e) => { e.stopPropagation(); onOpen(kind, id) }}
        >
            <OpenInFullIcon sx={{ fontSize: 15 }} />
        </IconButton>
    </Tooltip>
)

// The terminal Summary a quest/run stamps at a no-op terminal (e.g. a
// surfaced-only "N surfaced, 0 executed, 0 PRs"). Shown next to the status chip
// so a no-op terminal never reads as an undifferentiated "done". Mirrors how the
// detail views surface pauseReason.
const SummaryText = ({ summary }) => (
    summary
        ? <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.25, maxWidth: 240, whiteSpace: 'normal' }}>{summary}</Typography>
        : null
)

// A small outlined chip used for the non-PR delivery shapes in the PR column.
const DeliveryChip = ({ label, title }) => (
    <Chip size="small" variant="outlined" color="secondary" label={label} title={title} sx={{ height: 20, fontSize: 10 }} />
)

// PR cell. A run's delivery SHAPE (deliver) is its own terminal state — each is
// distinct from "has a PR" and from a PR-less (failed/pending) run, so none ever
// reads as a missing PR:
//   branch   — committed to a shared branch; the parent opens the PR.
//   feedback — updated an existing PR in place (links that PR).
//   review   — reviewed a PR; findings applied to it, or no actionable findings.
// `shape` is only passed for runs; quests fall through to count/url.
const PrCell = ({ url, count, shape }) => {
    const num = url ? url.split('/').pop() : null
    return (
        <TableCell onClick={(e) => e.stopPropagation()}>
            {shape === 'branch'
                ? <DeliveryChip label="committed" title="Committed to the shared branch — the parent opens the PR" />
                : shape === 'feedback'
                    ? (url
                        ? <CopyPrLink url={url} label={`updated PR #${num}`} />
                        : <DeliveryChip label="feedback applied" title="Addressed review feedback in place" />)
                    : shape === 'review'
                        ? (url
                            ? <CopyPrLink url={url} label={`reviewed #${num}`} />
                            : <DeliveryChip label="no findings" title="Reviewed — no actionable findings" />)
                        : url
                            ? <CopyPrLink url={url} />
                            : count
                                ? <Typography variant="caption" color="text.secondary">{count} PR{count > 1 ? 's' : ''}</Typography>
                                : <Typography variant="caption" color="text.secondary">—</Typography>}
        </TableCell>
    )
}

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
                            <Typography variant="body2" sx={{ fontWeight: 600, ...clamp2 }}>{runLabel(r)}</Typography>
                            {r.archived && <Chip size="small" variant="outlined" label="cleared" sx={{ height: 18, fontSize: 10 }} />}
                            <CopyReference kind="run" id={r.id} />
                            <OpenDetailButton kind="run" id={r.id} onOpen={onOpen} />
                        </Box>
                    </TableCell>
                    <TableCell>
                        <StatusChip item={r} status={r.status} text={statusText(r)} />
                        <SummaryText summary={r.summary} />
                    </TableCell>
                    <TableCell>
                        {r.questId
                            ? <ParentLink id={r.questId} level="quests" onPivot={onPivot} />
                            : <Typography variant="caption" color="text.secondary">—</Typography>}
                    </TableCell>
                    <TableCell><FolderText folder={folderOf(r)} /></TableCell>
                    <PrCell url={r.prUrl} shape={deliverOf(r)} />
                </TableRow>
            ))}
        </TableBody>
    </>
)

// Clicking a quest row drills the section down to that quest's child runs
// (parent-filtered), rather than opening the quest's overview modal.
const QuestsTable = ({ rows, onDrill, onOpen }) => (
    <>
        <TableHead>
            <TableRow>
                <TableCell sx={{ fontWeight: 700 }}>Objective</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Status</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>Progress</TableCell>
                <TableCell sx={{ fontWeight: 700 }}>PRs</TableCell>
            </TableRow>
        </TableHead>
        <TableBody>
            {rows.map((q) => (
                <TableRow key={q.id} hover onClick={() => onDrill('runs', q.id)} sx={{ cursor: 'pointer' }}>
                    <TableCell>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                            <Typography variant="body2" sx={{ fontWeight: 600, ...clamp2 }}>{questLabel(q)}</Typography>
                            <CopyReference kind="quest" id={q.id} />
                            <OpenDetailButton kind="quest" id={q.id} onOpen={onOpen} />
                        </Box>
                        <FolderText folder={folderOf(q)} />
                    </TableCell>
                    <TableCell>
                        <StatusChip item={q} status={q.status} text={statusText(q)} />
                        <SummaryText summary={q.summary} />
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

const COLSPAN = { runs: 5, quests: 4, adventures: 4 }

// Text fields each level is searched over. Adventures are perFinding quests, so
// they share the quest text fields.
const textFieldsFor = (item, level) => {
    if (level === 'runs') return [runLabel(item), item.status, folderOf(item), item.prompt, item.branch, item.id]
    return [item.objective, item.originalObjective, item.status, folderOf(item), item.id]
}

const Tasks = () => {
    const navigate = useNavigate()
    const [params, setParams] = useSearchParams()
    const level = LEVELS.some((l) => l.key === params.get('level')) ? params.get('level') : 'runs'
    const filters = readFilters(params)

    const runs = useRuns()
    const quests = useQuests()
    // Adventures are the perFinding quests; the Quests level shows ONLY the
    // non-perFinding (converge) quests. This is a strict partition — a perFinding
    // quest appears under Adventures and never under Quests.
    const items = level === 'runs' ? runs
        : level === 'adventures' ? quests.filter((q) => q.convergence === 'perFinding')
            : quests.filter((q) => q.convergence !== 'perFinding')
    // Adventures reuse the quest data shape for filtering/text-search.
    const dataLevel = level === 'adventures' ? 'quests' : level

    // Each run delivery SHAPE (branch / feedback / review) is its OWN PR state,
    // distinct from has-PR and from a PR-less/failed run. Handle them here on top of
    // the shared filters so a shaped run never collapses into "no PR": pr=<shape>
    // keeps only runs of that shape; pr=none excludes every shaped run (they're not
    // a missing PR — they legitimately deliver another way).
    const prState = filters.pr
    const SHAPE_FILTERS = ['branch', 'feedback', 'review']
    const filtered = useMemo(
        () => items.filter((it) => {
            if (level === 'runs') {
                const shape = deliverOf(it)
                const shaped = shape !== 'pr'
                if (SHAPE_FILTERS.includes(prState)) return shape === prState && matchFilters(it, { ...filters, pr: '' }, dataLevel, textFieldsFor(it, dataLevel))
                if (prState === 'none' && shaped) return false
            }
            return matchFilters(it, filters, dataLevel, textFieldsFor(it, dataLevel))
        }),
        [items, filters, level, dataLevel, prState],
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
    // Drill DOWN into an item's children: pivot to the child level filtered to
    // this item's id. Mechanically the same URL mutation as pivotToParent (set
    // level + parent), just invoked from a row click to go one level down.
    const pivotToChildren = pivotToParent
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

    // When the list is scoped to a specific parent (a drill-down from a quest
    // row, or a parent link), surface it as a removable chip — the Parent
    // dropdown alone is too easy to miss, and there was no obvious way to get back
    // to the unscoped list. 'none'/'any' are not a parent scope, so no chip.
    const scopedParent = filters.parent && filters.parent !== 'none' && filters.parent !== 'any' ? filters.parent : ''
    const parentEntity = scopedParent
        ? quests.find((q) => q.id === scopedParent)
        : null
    const parentKind = scopedParent ? 'quest' : ''
    const parentTitle = parentEntity
        ? (parentEntity.objective || parentEntity.originalObjective || '')
        : ''

    const empty = items.length === 0
        ? `No ${level} yet — they're launched from detritus.`
        : 'Nothing matches the active filters.'

    return (
        <Box>
            <Typography variant="h5" sx={{ fontWeight: 800 }}>Work</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                Every run and quest in any state. Pivot the level; filters carry across.
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
                onChange={setFilter}
                onClear={clearFilters}
            />

            {scopedParent && (
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2, flexWrap: 'wrap' }}>
                    <Typography variant="body2" color="text.secondary">Showing children of</Typography>
                    <Chip
                        color="secondary"
                        variant="outlined"
                        onDelete={() => setFilter('parent', '')}
                        label={`${parentKind} · ${scopedParent}${parentTitle ? ` — ${parentTitle}` : ''}`}
                        sx={{ maxWidth: '100%', '& .MuiChip-label': { overflow: 'hidden', textOverflow: 'ellipsis' } }}
                    />
                </Box>
            )}

            <Card sx={{ overflowX: 'auto' }}>
                <Table size="small" sx={{ minWidth: 720 }}>
                    {filtered.length === 0
                        ? <TableBody><TableRow><TableCell colSpan={COLSPAN[level]} sx={{ color: 'text.secondary' }}>{empty}</TableCell></TableRow></TableBody>
                        : level === 'runs' ? <RunsTable rows={filtered} onOpen={openDetail} onPivot={pivotToParent} />
                            : <QuestsTable rows={filtered} onDrill={pivotToChildren} onOpen={openDetail} />}
                </Table>
            </Card>
        </Box>
    )
}

export default Tasks
