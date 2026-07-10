import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Link from '@mui/material/Link'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import ClearIcon from '@mui/icons-material/Clear'
import VisibilityIcon from '@mui/icons-material/Visibility'
import CallMergeIcon from '@mui/icons-material/CallMerge'

import { candy } from '../config'
import { PHASES, STATUS_COLOR } from '../meta/run'
import { runLabel, questLabel, campaignLabel } from '../util'
import { useRuns, useQuests, useCampaigns, recency } from '../data/ooo'
import { archiveRun, archiveQuest, archiveCampaign } from '../data/api'
import { useToast } from '../feedback'
import { PauseChip } from '../components/StatusBits'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isTerminal = (r) => r.status === 'done' || r.status === 'cancelled'
// A babysit run doesn't build anything — after delivery it enters a watch phase,
// polling a single PR on an interval: fixing review feedback, merging once an
// approval lands. It's keyed on the delivery shape (or the presence of a live
// `watch` envelope from the conductor), and surfaces its OWN card on the landing
// with the watch state, tick log, and terminal outcome — never the build/task
// framing a normal run gets, which would read as a missing PR.
const isBabysit = (r) => r.deliver === 'babysit' || !!r.watch
const isParentRunning = (p) => p.status === 'running' || p.status === 'planning' || p.status === 'paused' || p.status === 'blocked'
// Actively working — no dismiss affordance. Anything else on the dashboard
// (blocked / paused / stopped / done / failed) can be dismissed to the Work history.
const isActive = (s) => s === 'running' || s === 'planning'

// Hard 2-line clamp for card titles — a legacy title-less item can carry a huge
// objective; the full text stays in the detail view's Objective & Intent tab.
const clamp2 = { display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden', wordBreak: 'break-word' }
const statusLabel = (r) => (r.status === 'done' ? 'Done' : r.status === 'cancelled' ? 'Cancelled' : (PHASES[r.phase] || r.status))

// Dismiss (archive) an item from the dashboard — it stays in the Work history.
// Only shown for non-running items, since dismissing live work would be surprising.
const DismissButton = ({ onDismiss }) => (
    <Tooltip title="Dismiss from dashboard — stays in Work">
        <IconButton size="small" aria-label="dismiss" onClick={(e) => { e.stopPropagation(); onDismiss() }} sx={{ flexShrink: 0 }}>
            <ClearIcon sx={{ fontSize: 16 }} />
        </IconButton>
    </Tooltip>
)

// A standalone run on the landing. Actively-running runs show status; a run that
// has stopped/blocked/failed can be dismissed to the Work history. The card is
// deliberately minimal — a title plus one status line — with the live per-agent
// detail reserved for the run workspace, not the landing.
const RunCard = ({ run, onOpen, onDismiss }) => (
    <Card onClick={() => onOpen(run.id)} sx={{ cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
        <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
            <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1 }}>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, ...clamp2 }}>{runLabel(run)}</Typography>
                {!isActive(run.status) && <DismissButton onDismiss={onDismiss} />}
            </Box>
            <Box sx={{ minWidth: 0 }}>
                <Typography variant="caption" color="secondary" sx={{ fontWeight: 700 }}>{statusLabel(run)}</Typography>
                <Typography variant="caption" color="text.secondary"> · {run.tasksGreen}/{run.tasksTotal} green · {run.accounting?.weightedTokens ?? run.tokensUsed}k tok · ${(run.accounting?.costUsd ?? run.costUsd ?? 0).toFixed(2)}</Typography>
            </Box>
            {run.status === 'paused' && <Box sx={{ mt: 0.75 }}><PauseChip entity={run} /></Box>}
        </CardContent>
    </Card>
)

// A running campaign/quest PARENT. The dashboard is a calm, MINIMAL overview: a
// short title plus the AGGREGATED state (how many child runs and their combined
// green count) — never a per-child or per-agent breakdown. The full breakdown
// lives in the campaign/quest detail view, which the card drills into via
// onOpenParent. This keeps the landing scannable.
const ParentCard = ({ parent, kind, title, children, onOpenParent, onDismiss }) => {
    const greenT = children.reduce((n, r) => n + (r.tasksGreen || 0), 0)
    const totalT = children.reduce((n, r) => n + (r.tasksTotal || 0), 0)
    return (
        <Card
            onClick={() => onOpenParent(kind, parent.id)}
            sx={{ borderColor: 'secondary.main', cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}
        >
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.25 }}>
                    <Chip size="small" color="secondary" variant="outlined" label={`${kind} · ${parent.id}`} sx={{ height: 20 }} />
                    {parent.status === 'paused'
                        ? <PauseChip entity={parent} sx={{ height: 20 }} />
                        : <Chip size="small" color={STATUS_COLOR[parent.status] || 'default'} variant="outlined" label={parent.status} sx={{ height: 20 }} />}
                    <Box sx={{ flexGrow: 1 }} />
                    {!isActive(parent.status) && <DismissButton onDismiss={onDismiss} />}
                </Box>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, ...clamp2 }}>{title}</Typography>
                <Box sx={{ mt: 0.5 }}>
                    <Typography variant="caption" color="text.secondary">{children.length} run{children.length === 1 ? '' : 's'} · {greenT}/{totalT} green</Typography>
                </Box>
            </CardContent>
        </Card>
    )
}

// ── Babysit watch surface ────────────────────────────────────────────────────
// The watch phase is a small state machine, honestly rendered so none of its
// states reads as a failure: watching (idle poll), fixing (addressing feedback),
// merged (the terminal success), stopped (the terminal give-up). Unknown values
// fall back to the neutral watching styling so the card never crashes.
const WATCH_STATE = {
    watching: { label: 'Watching', color: candy.sky },
    fixing: { label: 'Fixing feedback', color: candy.lemon },
    merged: { label: 'Merged', color: candy.mint },
    stopped: { label: 'Stopped', color: candy.grape },
}
const watchMeta = (s) => WATCH_STATE[s] || WATCH_STATE.watching
const isWatchTerminal = (s) => s === 'merged' || s === 'stopped'

// One dot colour per tick decision — the tick log is an append-only trail of what
// each interval poll observed, newest first in the surface. Keys match the
// WatchDecision values the conductor serializes (wait|feedback|merge|done).
const TICK_DOT = { wait: candy.line, feedback: candy.lemon, merge: candy.mint, done: candy.mint }
const tickDot = (k) => TICK_DOT[k] || candy.line

const tickTime = (at) => {
    if (!at) return ''
    const d = new Date(at)
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const prNumber = (url) => (url ? `#${String(url).split('/').pop()}` : '')

// The terminal outcome, once the watch has ended — a positive, honest close:
// merged (the PR landed) or stopped (the watch was ended without a merge). While
// the watch is live this renders nothing.
// state is the terminal watch lifecycle state (merged|stopped); outcome is the
// backend's human sentence (e.g. 'PR #7 merged on approval.'), surfaced as the
// chip's tooltip. The chip variant keys off state, never the free-text outcome.
const WatchOutcome = ({ state, outcome }) => {
    if (state === 'merged') return <Chip icon={<CallMergeIcon />} size="small" color="success" variant="outlined" label="Merged" title={outcome || undefined} sx={{ flexShrink: 0 }} />
    if (state === 'stopped') return <Chip size="small" color="default" variant="outlined" label="Watch stopped" title={outcome || undefined} sx={{ flexShrink: 0 }} />
    return null
}

// The compact tick log — the last few interval polls, newest first. Kept short
// on the landing surface; the full trail lives in the run workspace.
const TickLog = ({ ticks }) => {
    const recent = [...ticks].reverse().slice(0, 4)
    if (recent.length === 0) return <Typography variant="caption" color="text.secondary">No ticks yet — first poll pending.</Typography>
    return (
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
            {recent.map((t, i) => (
                <Box key={i} sx={{ display: 'flex', alignItems: 'baseline', gap: 1, minWidth: 0 }}>
                    <Box sx={{ width: 7, height: 7, borderRadius: '50%', backgroundColor: tickDot(t.decision), flexShrink: 0, alignSelf: 'center' }} />
                    <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace', flexShrink: 0 }}>{tickTime(t.at)}</Typography>
                    <Typography variant="caption" color="text.secondary" sx={{ minWidth: 0, ...clamp2, WebkitLineClamp: 1 }}>{t.detail}</Typography>
                </Box>
            ))}
        </Box>
    )
}

// A babysit run's landing surface: the PR under watch, the current watch state,
// the tick log, and — once ended — the terminal outcome. Only a terminal watch
// can be dismissed to the Work history; a live watch has no dismiss affordance,
// mirroring how actively-running work behaves elsewhere on the dashboard.
const BabysitCard = ({ run, onOpen, onDismiss }) => {
    const watch = run.watch || {}
    const state = watch.state || (isTerminal(run) ? (watch.outcome || 'stopped') : 'watching')
    const meta = watchMeta(state)
    const terminal = isWatchTerminal(state) || isTerminal(run)
    const prUrl = watch.prUrl || run.prUrl
    const ticks = Array.isArray(watch.ticks) ? watch.ticks : []
    return (
        <Card onClick={() => onOpen(run.id)} sx={{ borderColor: 'secondary.main', cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.25 }}>
                    <Chip icon={<VisibilityIcon />} size="small" variant="outlined" label="babysit" sx={{ height: 20, borderColor: meta.color, color: meta.color, '& .MuiChip-icon': { color: meta.color } }} />
                    <Chip size="small" variant="outlined" label={meta.label} sx={{ height: 20, borderColor: meta.color, color: meta.color }} />
                    <Box sx={{ flexGrow: 1 }} />
                    {terminal && <WatchOutcome state={isWatchTerminal(state) ? state : 'stopped'} outcome={watch.outcome} />}
                    {terminal && <DismissButton onDismiss={onDismiss} />}
                </Box>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, ...clamp2 }}>{runLabel(run)}</Typography>
                <Box sx={{ mt: 0.25, mb: 1, display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                    {prUrl
                        ? <Link href={prUrl} target="_blank" rel="noopener" onClick={(e) => e.stopPropagation()} sx={{ fontFamily: 'monospace', fontSize: 12 }}>PR {prNumber(prUrl)}</Link>
                        : <Typography variant="caption" color="text.secondary">PR pending</Typography>}
                    {watch.interval ? <Typography variant="caption" color="text.secondary">· every {watch.interval}s</Typography> : null}
                    <Typography variant="caption" color="text.secondary">· {ticks.length} tick{ticks.length === 1 ? '' : 's'}</Typography>
                </Box>
                <TickLog ticks={ticks} />
            </CardContent>
        </Card>
    )
}

const Landing = ({ runs, campaigns, quests, onOpen, onOpenParent, onDismiss }) => {
    // Parents: running campaigns, then running quests NOT owned by a shown campaign
    // (a campaign-owned quest's runs already aggregate under the campaign).
    const runningCampaigns = campaigns.filter(isParentRunning)
    const campaignIds = new Set(runningCampaigns.map((c) => c.id))
    const runningQuests = quests.filter((q) => isParentRunning(q) && !(q.campaignId && campaignIds.has(q.campaignId)))

    const childrenOfCampaign = (c) => runs.filter((r) => r.campaignId === c.id)
    const childrenOfQuest = (q) => runs.filter((r) => r.questId === q.id)

    // The landing is a calm overview of ACTIVE work only. Standalone runs that are
    // still going surface as cards; finished (done/cancelled) runs are not a dump
    // here — they live in the Work history. useRuns is already newest-first.
    // Babysit runs get their own watch surface and are handled separately, so
    // filter them out of the generic run flow to avoid a double render. A babysit
    // run stays surfaced through its terminal outcome (merged/stopped) until it's
    // dismissed, unlike a normal run which drops off the landing once terminal.
    const babysits = runs.filter((r) => !r.campaignId && !r.questId && isBabysit(r))
    const running = runs.filter((r) => !r.campaignId && !r.questId && !isBabysit(r) && !isTerminal(r))

    const nothing = runningCampaigns.length === 0 && runningQuests.length === 0 && running.length === 0 && babysits.length === 0

    // Interleave all active work by recency (ooo envelope `updated`, falling back
    // to `created`) so the MOST RECENTLY changed item leads regardless of its type
    // — campaigns, quests, and standalone runs share one newest-first ordering
    // rather than being grouped by type (which sank a just-launched run below older
    // programs).
    const entries = [
        ...runningCampaigns.map((c) => ({
            r: recency(c),
            node: (
                <ParentCard
                    key={`campaign-${c.id}`} parent={c} kind="campaign"
                    title={campaignLabel(c)}
                    children={childrenOfCampaign(c)} onOpenParent={onOpenParent}
                    onDismiss={() => onDismiss('campaign', c.id)}
                />
            ),
        })),
        ...runningQuests.map((q) => ({
            r: recency(q),
            node: (
                <ParentCard
                    key={`quest-${q.id}`} parent={q} kind="quest"
                    title={questLabel(q)}
                    children={childrenOfQuest(q)} onOpenParent={onOpenParent}
                    onDismiss={() => onDismiss('quest', q.id)}
                />
            ),
        })),
        ...running.map((run) => ({ r: recency(run), node: <RunCard key={`run-${run.id}`} run={run} onOpen={onOpen} onDismiss={() => onDismiss('run', run.id)} /> })),
        ...babysits.map((run) => ({ r: recency(run), node: <BabysitCard key={`babysit-${run.id}`} run={run} onOpen={onOpen} onDismiss={() => onDismiss('run', run.id)} /> })),
    ].sort((a, b) => b.r - a.r)

    return (
        <Box>
            <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="secondary">what's going on</Typography>
                <Typography variant="caption" color="text.secondary">
                    {runningCampaigns.length + runningQuests.length} active program{runningCampaigns.length + runningQuests.length === 1 ? '' : 's'} · {running.length} standalone running{babysits.length ? ` · ${babysits.length} watching` : ''}
                </Typography>
            </Box>

            {nothing ? (
                <Typography variant="body2" color="text.secondary">Nothing running. Launch a run, quest, or campaign from detritus to see it here.</Typography>
            ) : (
                <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' }, gap: 2 }}>
                    {entries.map((e) => e.node)}
                </Box>
            )}
        </Box>
    )
}

const ARCHIVE = { run: archiveRun, quest: archiveQuest, campaign: archiveCampaign }

const Dashboard = () => {
    const navigate = useNavigate()
    const toast = useToast()
    const { runId, tab } = useParams()
    const liveRuns = useRuns()
    const liveCampaigns = useCampaigns()
    const liveQuests = useQuests()

    // Archived items are cleared from the dashboard but kept in the Work history.
    const runs = liveRuns.filter((r) => !r.archived)
    const campaigns = liveCampaigns.filter((c) => !c.archived)
    const quests = liveQuests.filter((q) => !q.archived)

    // Dismiss = archive: hide from the dashboard, keep in Work. ooo pushes the
    // archived flag back and the filters above drop it on the next render.
    const dismiss = (kind, id) => ARCHIVE[kind](id).catch((e) => toast(e?.message || 'Dismiss failed — is the candyland server reachable?'))

    return (
        <Box>
            <Landing
                runs={runs}
                campaigns={campaigns}
                quests={quests}
                onOpen={(id) => navigate(`/run/${id}`)}
                onOpenParent={(kind, id) => navigate(`/${kind}/${id}`)}
                onDismiss={dismiss}
            />

            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
