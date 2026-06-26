import React from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import AddIcon from '@mui/icons-material/Add'
import ClearIcon from '@mui/icons-material/Clear'

import { candy } from '../config'
import { useMode } from '../mode'
import { PHASES, STATE_META } from '../meta/run'
import { runLabel } from '../util'
import { useRuns } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { createRun, archiveRun } from '../data/api'
import { useToast } from '../feedback'
import { ModeBadge } from '../components/StatusBits'
import NewRunWizard from '../wizard/NewRunWizard'
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
                    <ModeBadge mode={run.mode} />
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

const Landing = ({ runs, onClear, onOpen, onNew, reachable }) => {
    const running = runs.filter((r) => !isTerminal(r))
    const recentTerminal = runs.filter(isTerminal).slice(0, RECENT_TERMINAL)
    const visible = [...running, ...recentTerminal] // useRuns is already newest-first

    return (
        <Box>
            {/* candyland is the sidecar: you launch runs from your editor (the candyland
                MCP uses your current folder), and monitor / audit / stop them here. */}
            <Card sx={{ mb: 5, borderColor: 'primary.main' }}>
                <CardContent sx={{ display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap', py: 3 }}>
                    <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                        <Typography variant="h5" sx={{ fontWeight: 800 }}>Launch from your editor</Typography>
                        <Typography variant="body2" color="text.secondary">
                            Run <Box component="span" sx={{ fontFamily: 'monospace', color: 'primary.main' }}>launch_run</Box> from your Claude Code session (the candyland MCP) to start a multi-agent run in your current folder. Monitor, audit, and stop it here.
                        </Typography>
                    </Box>
                    {/* Secondary path: start one from here by naming the repo folder. */}
                    <Tooltip title={reachable ? '' : 'Server unreachable — start ./candyland first'} disableHoverListener={reachable}>
                        <Box sx={{ flexShrink: 0 }}>
                            <Button variant="outlined" startIcon={<AddIcon />} onClick={onNew} disabled={!reachable}>Start one here</Button>
                        </Box>
                    </Tooltip>
                </CardContent>
            </Card>

            <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="secondary">what's going on</Typography>
                <Typography variant="caption" color="text.secondary">{running.length} running · {recentTerminal.length} recent</Typography>
            </Box>

            {visible.length === 0 ? (
                <Typography variant="body2" color="text.secondary">Nothing running. Launch a run from your editor (or start one here) to see it.</Typography>
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
    const location = useLocation()
    const { runId, tab } = useParams()
    const { mode } = useMode()
    const liveRuns = useRuns()
    const { reachable } = useSystemStatus()
    const toast = useToast()

    // Archived runs are cleared from the dashboard but kept in the Tasks history.
    const runs = liveRuns.filter((r) => !r.archived)
    const isNew = location.pathname === '/new'

    const start = async ({ folders, prompt, title }) => {
        if (!reachable) {
            toast('Server unreachable — start ./candyland, then try again.')
            return
        }
        try {
            const { id } = await createRun({ mode, folders, prompt, title })
            navigate(`/run/${id}`)
        } catch {
            toast("Couldn't start the run — is the candyland server reachable? Check the status chip.")
        }
    }

    return (
        <Box>
            <Landing
                runs={runs}
                reachable={reachable}
                onNew={() => navigate('/new')}
                onOpen={(id) => navigate(`/run/${id}`)}
                onClear={(id) => archiveRun(id).catch(() => toast("Couldn't clear the run."))}
            />

            {isNew && <NewRunWizard onClose={() => navigate('/')} onStart={start} />}
            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
