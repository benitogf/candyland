import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Typography from '@mui/material/Typography'

import { candy } from '../config'
import { PHASES, STATE_META, STATUS_COLOR } from '../meta/run'
import { runLabel } from '../util'
import { useRuns, useQuests, useCampaigns } from '../data/ooo'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isTerminal = (r) => r.status === 'done' || r.status === 'cancelled'
const isParentRunning = (p) => p.status === 'running' || p.status === 'planning' || p.status === 'paused' || p.status === 'blocked'
const statusLabel = (r) => (r.status === 'done' ? 'Done' : r.status === 'cancelled' ? 'Cancelled' : (PHASES[r.phase] || r.status))

const FleetDots = ({ agents = [] }) => (
    <Box sx={{ display: 'flex', gap: 0.5, flexShrink: 0 }}>
        {agents.length === 0
            ? <Typography variant="caption" color="text.secondary">planning…</Typography>
            : agents.map((a) => (
                <Box key={a.id} title={`${a.role} · ${STATE_META[a.state]?.label || a.state}`} sx={{ width: 10, height: 10, borderRadius: '50%', backgroundColor: STATE_META[a.state]?.dot || candy.line }} />
            ))}
    </Box>
)

// A standalone active run. The landing only ever shows active work, so there is
// no terminal / clear affordance here — finished runs live in the Work history.
const RunCard = ({ run, onOpen }) => (
    <Card onClick={() => onOpen(run.id)} sx={{ cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
        <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
            <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1 }}>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, wordBreak: 'break-word' }}>{runLabel(run)}</Typography>
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
const ParentCard = ({ parent, kind, title, children, onOpenParent }) => {
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

const Landing = ({ runs, campaigns, quests, onOpen, onOpenParent }) => {
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
                    {runningCampaigns.map((c) => (
                        <ParentCard
                            key={c.id} parent={c} kind="campaign"
                            title={c.intentBrief?.restatedGoal || c.originalInput || c.id}
                            children={childrenOfCampaign(c)} onOpenParent={onOpenParent}
                        />
                    ))}
                    {runningQuests.map((q) => (
                        <ParentCard
                            key={q.id} parent={q} kind="quest"
                            title={q.objective || q.originalObjective || q.id}
                            children={childrenOfQuest(q)} onOpenParent={onOpenParent}
                        />
                    ))}
                    {running.map((run) => <RunCard key={run.id} run={run} onOpen={onOpen} />)}
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
            />

            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
