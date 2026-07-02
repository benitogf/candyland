import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import ClearIcon from '@mui/icons-material/Clear'

import { candy } from '../config'
import { PHASES, STATE_META, STATUS_COLOR } from '../meta/run'
import { runLabel } from '../util'
import { useRuns, useQuests, useCampaigns, recency } from '../data/ooo'
import { archiveRun, archiveQuest, archiveCampaign } from '../data/api'
import { useToast } from '../feedback'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isTerminal = (r) => r.status === 'done' || r.status === 'cancelled'
const isParentRunning = (p) => p.status === 'running' || p.status === 'planning' || p.status === 'paused' || p.status === 'blocked'
// Actively working — no dismiss affordance. Anything else on the dashboard
// (blocked / paused / stopped / done / failed) can be dismissed to the Work history.
const isActive = (s) => s === 'running' || s === 'planning'
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

const FleetDots = ({ agents = [] }) => (
    <Box sx={{ display: 'flex', gap: 0.5, flexShrink: 0 }}>
        {agents.length === 0
            ? <Typography variant="caption" color="text.secondary">planning…</Typography>
            : agents.map((a) => (
                <Box key={a.id} title={`${a.role} · ${STATE_META[a.state]?.label || a.state}`} sx={{ width: 10, height: 10, borderRadius: '50%', backgroundColor: STATE_META[a.state]?.dot || candy.line }} />
            ))}
    </Box>
)

// A standalone run on the landing. Actively-running runs show status + fleet;
// a run that has stopped/blocked/failed can be dismissed to the Work history.
const RunCard = ({ run, onOpen, onDismiss }) => (
    <Card onClick={() => onOpen(run.id)} sx={{ cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
        <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
            <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1 }}>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, wordBreak: 'break-word' }}>{runLabel(run)}</Typography>
                {!isActive(run.status) && <DismissButton onDismiss={onDismiss} />}
            </Box>
            <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1 }}>
                <Box sx={{ minWidth: 0 }}>
                    <Typography variant="caption" color="secondary" sx={{ fontWeight: 700 }}>{statusLabel(run)}</Typography>
                    <Typography variant="caption" color="text.secondary"> · {run.tasksGreen}/{run.tasksTotal} green · {run.tokensUsed}k tok</Typography>
                </Box>
                <FleetDots agents={run.agents} />
            </Box>
        </CardContent>
    </Card>
)

// A running campaign/quest PARENT. The dashboard is a calm, MINIMAL overview: a
// short title plus the AGGREGATED state (how many child runs, their combined green
// count, and one fleet-dot row across all their agents) — never a per-child
// breakdown. The full breakdown lives in the campaign/quest detail view, which the
// card drills into via onOpenParent. This keeps the landing scannable.
const ParentCard = ({ parent, kind, title, children, onOpenParent, onDismiss }) => {
    const greenT = children.reduce((n, r) => n + (r.tasksGreen || 0), 0)
    const totalT = children.reduce((n, r) => n + (r.tasksTotal || 0), 0)
    const agents = children.flatMap((r) => r.agents || [])
    return (
        <Card
            onClick={() => onOpenParent(kind, parent.id)}
            sx={{ borderColor: 'secondary.main', cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}
        >
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.25 }}>
                    <Chip size="small" color="secondary" variant="outlined" label={`${kind} · ${parent.id}`} sx={{ height: 20 }} />
                    <Chip size="small" color={STATUS_COLOR[parent.status] || 'default'} variant="outlined" label={parent.status} sx={{ height: 20 }} />
                    <Box sx={{ flexGrow: 1 }} />
                    {!isActive(parent.status) && <DismissButton onDismiss={onDismiss} />}
                </Box>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, wordBreak: 'break-word' }}>{title}</Typography>
                <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1, mt: 0.5 }}>
                    <Typography variant="caption" color="text.secondary">{children.length} run{children.length === 1 ? '' : 's'} · {greenT}/{totalT} green</Typography>
                    <FleetDots agents={agents} />
                </Box>
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
    const running = runs.filter((r) => !r.campaignId && !r.questId && !isTerminal(r))

    const nothing = runningCampaigns.length === 0 && runningQuests.length === 0 && running.length === 0

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
                    title={c.intentBrief?.restatedGoal || c.originalInput || c.id}
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
                    title={q.objective || q.originalObjective || q.id}
                    children={childrenOfQuest(q)} onOpenParent={onOpenParent}
                    onDismiss={() => onDismiss('quest', q.id)}
                />
            ),
        })),
        ...running.map((run) => ({ r: recency(run), node: <RunCard key={`run-${run.id}`} run={run} onOpen={onOpen} onDismiss={() => onDismiss('run', run.id)} /> })),
    ].sort((a, b) => b.r - a.r)

    return (
        <Box>
            <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="secondary">what's going on</Typography>
                <Typography variant="caption" color="text.secondary">
                    {runningCampaigns.length + runningQuests.length} active program{runningCampaigns.length + runningQuests.length === 1 ? '' : 's'} · {running.length} standalone running
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
