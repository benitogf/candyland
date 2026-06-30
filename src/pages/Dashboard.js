import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import ClearIcon from '@mui/icons-material/Clear'

import { candy } from '../config'
import { PHASES, STATE_META } from '../meta/run'
import { runLabel } from '../util'
import { useRuns } from '../data/ooo'
import { archiveRun } from '../data/api'
import { useToast } from '../feedback'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isTerminal = (r) => r.status === 'done' || r.status === 'cancelled'
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

const Landing = ({ runs, onClear, onOpen }) => {
    const running = runs.filter((r) => !isTerminal(r))
    const recentTerminal = runs.filter(isTerminal).slice(0, RECENT_TERMINAL)
    const visible = [...running, ...recentTerminal] // useRuns is already newest-first

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
                <Typography variant="caption" color="text.secondary">{running.length} running · {recentTerminal.length} recent</Typography>
            </Box>

            {visible.length === 0 ? (
                <Typography variant="body2" color="text.secondary">Nothing running. Launch a run from detritus to see it here.</Typography>
            ) : (
                <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' }, gap: 2 }}>
                    {visible.map((run) => <RunCard key={run.id} run={run} onOpen={onOpen} onClear={onClear} />)}
                </Box>
            )}
        </Box>
    )
}

const Dashboard = () => {
    const navigate = useNavigate()
    const { runId, tab } = useParams()
    const liveRuns = useRuns()
    const toast = useToast()

    // Archived runs are cleared from the dashboard but kept in the Tasks history.
    const runs = liveRuns.filter((r) => !r.archived)

    return (
        <Box>
            <Landing
                runs={runs}
                onOpen={(id) => navigate(`/run/${id}`)}
                onClear={(id) => archiveRun(id).catch(() => toast("Couldn't clear the run."))}
            />

            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
