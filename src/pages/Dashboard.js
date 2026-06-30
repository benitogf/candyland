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

import { candy } from '../config'
import { PHASES, STATE_META, STATUS_COLOR } from '../meta/run'
import { runLabel } from '../util'
import { useRuns, useQuests, useCampaigns, isBranchDelivered } from '../data/ooo'
import { archiveRun } from '../data/api'
import { useToast } from '../feedback'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isTerminal = (r) => r.status === 'done' || r.status === 'cancelled'
const isParentRunning = (p) => p.status === 'running' || p.status === 'planning' || p.status === 'paused' || p.status === 'blocked'
const statusLabel = (r) => (r.status === 'done' ? 'Done' : r.status === 'cancelled' ? 'Cancelled' : (PHASES[r.phase] || r.status))
const RECENT_TERMINAL = 4

const FleetDots = ({ agents = [] }) => (
    <Box sx={{ display: 'flex', gap: 0.5, flexShrink: 0 }}>
        {agents.length === 0
            ? <Typography variant="caption" color="text.secondary">planning…</Typography>
            : agents.map((a) => (
                <Box key={a.id} title={`${a.role} · ${STATE_META[a.state]?.label || a.state}`} sx={{ width: 10, height: 10, borderRadius: '50%', backgroundColor: STATE_META[a.state]?.dot || candy.line }} />
            ))}
    </Box>
)

const RunCard = ({ run, onOpen, onClear }) => {
    const terminal = isTerminal(run)
    return (
        <Card onClick={() => onOpen(run.id)} sx={{ cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1 }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, wordBreak: 'break-word' }}>{runLabel(run)}</Typography>
                    {terminal && (
                        <Tooltip title="Clear from dashboard (kept in Tasks)">
                            <IconButton size="small" onClick={(e) => { e.stopPropagation(); onClear(run.id) }} aria-label="clear run" sx={{ ml: -0.5, mt: -0.5 }}>
                                <ClearIcon fontSize="small" />
                            </IconButton>
                        </Tooltip>
                    )}
                </Box>
                <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1 }}>
                    <Box sx={{ minWidth: 0 }}>
                        <Typography variant="caption" color={terminal ? 'text.secondary' : 'secondary'} sx={{ fontWeight: 700 }}>{statusLabel(run)}</Typography>
                        <Typography variant="caption" color="text.secondary"> · {run.tasksGreen}/{run.tasksTotal} green · {run.tokensUsed}k tok</Typography>
                    </Box>
                    <FleetDots agents={run.agents} />
                </Box>
            </CardContent>
        </Card>
    )
}

// One child run under a parent — a compact, clickable row that drills into the
// EXISTING run UI (/run/:id via onOpen). Branch-delivered children read as
// "committed", never as a missing PR.
const ChildRunRow = ({ run, onOpen }) => {
    const terminal = isTerminal(run)
    return (
        <Box
            onClick={() => onOpen(run.id)}
            sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.75, px: 1, borderRadius: 1, cursor: 'pointer', '&:hover': { backgroundColor: candy.bgPaperHi } }}
        >
            <Typography variant="body2" sx={{ fontWeight: 600, minWidth: 0, flexGrow: 1, wordBreak: 'break-word' }}>{runLabel(run)}</Typography>
            <Typography variant="caption" color={terminal ? 'text.secondary' : 'secondary'} sx={{ fontWeight: 700, flexShrink: 0 }}>{statusLabel(run)}</Typography>
            {isBranchDelivered(run)
                ? <Chip size="small" variant="outlined" color="secondary" label="committed" sx={{ height: 18, fontSize: 10, flexShrink: 0 }} />
                : run.prUrl && <Link href={run.prUrl} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()} sx={{ fontSize: 12, flexShrink: 0 }}>PR</Link>}
            <FleetDots agents={run.agents} />
        </Box>
    )
}

// A running campaign/quest PARENT, aggregating its child runs. The header drills
// into the existing campaign/quest detail (onOpenParent); each child row drills
// into the existing run UI (onOpen). This is an aggregation + navigation layer
// over the existing run/task presentation, not a new observability stack.
const ParentCard = ({ parent, kind, title, children, onOpen, onOpenParent }) => {
    const greenT = children.reduce((n, r) => n + (r.tasksGreen || 0), 0)
    const totalT = children.reduce((n, r) => n + (r.tasksTotal || 0), 0)
    return (
        <Card sx={{ borderColor: 'secondary.main' }}>
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box
                    onClick={() => onOpenParent(kind, parent.id)}
                    sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1, cursor: 'pointer' }}
                >
                    <Box sx={{ minWidth: 0, flexGrow: 1 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.25 }}>
                            <Chip size="small" color="secondary" variant="outlined" label={`${kind} · ${parent.id}`} sx={{ height: 20 }} />
                            <Chip size="small" color={STATUS_COLOR[parent.status] || 'default'} variant="outlined" label={parent.status} sx={{ height: 20 }} />
                        </Box>
                        <Typography variant="subtitle1" sx={{ fontWeight: 700, wordBreak: 'break-word' }}>{title}</Typography>
                        <Typography variant="caption" color="text.secondary">{children.length} run{children.length === 1 ? '' : 's'} · {greenT}/{totalT} green</Typography>
                    </Box>
                </Box>
                {children.length === 0
                    ? <Typography variant="caption" color="text.secondary" sx={{ pl: 1 }}>No child runs launched yet.</Typography>
                    : children.map((r) => <ChildRunRow key={r.id} run={r} onOpen={onOpen} />)}
            </CardContent>
        </Card>
    )
}

const Landing = ({ runs, campaigns, quests, onClear, onOpen, onOpenParent }) => {
    // Parents: running campaigns, then running quests NOT owned by a shown campaign
    // (a campaign-owned quest's runs already aggregate under the campaign).
    const runningCampaigns = campaigns.filter(isParentRunning)
    const campaignIds = new Set(runningCampaigns.map((c) => c.id))
    const runningQuests = quests.filter((q) => isParentRunning(q) && !(q.campaignId && campaignIds.has(q.campaignId)))

    const childrenOfCampaign = (c) => runs.filter((r) => r.campaignId === c.id)
    const childrenOfQuest = (q) => runs.filter((r) => r.questId === q.id)

    // Standalone runs: not owned by any parent. Running ones surface as cards; a
    // few recent terminal ones too — exactly the prior behaviour for these.
    const standalone = runs.filter((r) => !r.campaignId && !r.questId)
    const running = standalone.filter((r) => !isTerminal(r))
    const recentTerminal = standalone.filter(isTerminal).slice(0, RECENT_TERMINAL)
    const visibleRuns = [...running, ...recentTerminal] // useRuns is already newest-first

    const nothing = runningCampaigns.length === 0 && runningQuests.length === 0 && visibleRuns.length === 0

    return (
        <Box>
            {/* candyland is observe-only: detritus launches runs (over REST); this
                dashboard monitors / audits / stops the runs it observes. */}
            <Card sx={{ mb: 5, borderColor: 'primary.main' }}>
                <CardContent sx={{ py: 3 }}>
                    <Typography variant="h5" sx={{ fontWeight: 800 }}>Launched from detritus</Typography>
                    <Typography variant="body2" color="text.secondary">
                        Runs are started by detritus over REST. Monitor, audit, and stop them here.
                    </Typography>
                </CardContent>
            </Card>

            <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="secondary">what's going on</Typography>
                <Typography variant="caption" color="text.secondary">
                    {runningCampaigns.length + runningQuests.length} active program{runningCampaigns.length + runningQuests.length === 1 ? '' : 's'} · {running.length} standalone running · {recentTerminal.length} recent
                </Typography>
            </Box>

            {nothing ? (
                <Typography variant="body2" color="text.secondary">Nothing running. Launch a run, quest, or campaign from detritus to see it here.</Typography>
            ) : (
                <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' }, gap: 2 }}>
                    {runningCampaigns.map((c) => (
                        <ParentCard
                            key={c.id} parent={c} kind="campaign"
                            title={c.intentBrief?.restatedGoal || c.originalInput || c.id}
                            children={childrenOfCampaign(c)} onOpen={onOpen} onOpenParent={onOpenParent}
                        />
                    ))}
                    {runningQuests.map((q) => (
                        <ParentCard
                            key={q.id} parent={q} kind="quest"
                            title={q.objective || q.originalObjective || q.id}
                            children={childrenOfQuest(q)} onOpen={onOpen} onOpenParent={onOpenParent}
                        />
                    ))}
                    {visibleRuns.map((run) => <RunCard key={run.id} run={run} onOpen={onOpen} onClear={onClear} />)}
                </Box>
            )}
        </Box>
    )
}

const Dashboard = () => {
    const navigate = useNavigate()
    const { runId, tab } = useParams()
    const liveRuns = useRuns()
    const campaigns = useCampaigns()
    const quests = useQuests()
    const toast = useToast()

    // Archived runs are cleared from the dashboard but kept in the Tasks history.
    const runs = liveRuns.filter((r) => !r.archived)

    return (
        <Box>
            <Landing
                runs={runs}
                campaigns={campaigns}
                quests={quests}
                onOpen={(id) => navigate(`/run/${id}`)}
                onOpenParent={(kind, id) => navigate(`/${kind}/${id}`)}
                onClear={(id) => archiveRun(id).catch(() => toast("Couldn't clear the run."))}
            />

            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
