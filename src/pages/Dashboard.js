import React, { useState } from 'react'
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
import FolderIcon from '@mui/icons-material/Folder'

import { candy } from '../config'
import { useMode } from '../mode'
import { PHASES, WORKSPACES, STATE_META, workspaceById } from '../mock/run'
import { runLabel } from '../util'
import { useRuns } from '../data/ooo'
import { createRun } from '../data/api'
import { ModeBadge } from '../components/StatusBits'
import NewRunWizard from '../wizard/NewRunWizard'
import { LiveRunWorkspace } from '../dashboard/RunHost'

const isDoneRun = (r) => r.status === 'done'
const RECENT_DONE = 4

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
    const done = isDoneRun(run)
    return (
        <Card onClick={() => onOpen(run.id)} sx={{ cursor: 'pointer', transition: 'background-color 120ms', '&:hover': { backgroundColor: candy.bgPaperHi } }}>
            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, mb: 1 }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, wordBreak: 'break-word' }}>{runLabel(run)}</Typography>
                    <ModeBadge mode={run.mode} />
                    {done && (
                        <Tooltip title="Clear from dashboard">
                            <IconButton size="small" onClick={(e) => { e.stopPropagation(); onClear(run.id) }} aria-label="clear run" sx={{ ml: -0.5, mt: -0.5 }}>
                                <ClearIcon fontSize="small" />
                            </IconButton>
                        </Tooltip>
                    )}
                </Box>
                <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1 }}>
                    <Box sx={{ minWidth: 0 }}>
                        <Typography variant="caption" color={done ? 'text.secondary' : 'secondary'} sx={{ fontWeight: 700 }}>{done ? 'Done' : PHASES[run.phase] || run.status}</Typography>
                        <Typography variant="caption" color="text.secondary"> · {run.tasksGreen}/{run.tasksTotal} green · {run.tokensUsed}k tok</Typography>
                    </Box>
                    <FleetDots agents={run.agents} />
                </Box>
            </CardContent>
        </Card>
    )
}

const Landing = ({ runs, onClear, onOpen, onNew }) => {
    const running = runs.filter((r) => !isDoneRun(r))
    const recentDone = runs.filter(isDoneRun).slice(0, RECENT_DONE)
    const visible = [...running, ...recentDone]
    const groups = WORKSPACES
        .map((ws) => ({ ws, items: visible.filter((r) => r.workspace === ws.id) }))
        .filter((g) => g.items.length)

    return (
        <Box>
            <Card sx={{ mb: 5, borderColor: 'primary.main' }}>
                <CardContent sx={{ display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap', py: 3 }}>
                    <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                        <Typography variant="h5" sx={{ fontWeight: 800 }}>Start something new</Typography>
                        <Typography variant="body2" color="text.secondary">Describe what you want built — we'll guide you through it and hand back one PR.</Typography>
                    </Box>
                    <Button size="large" variant="contained" startIcon={<AddIcon />} onClick={onNew} sx={{ flexShrink: 0 }}>Start a new run</Button>
                </CardContent>
            </Card>

            <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="secondary">what's going on</Typography>
                <Typography variant="caption" color="text.secondary">{running.length} running · {recentDone.length} recent</Typography>
            </Box>

            {groups.length === 0 ? (
                <Typography variant="body2" color="text.secondary">Nothing running. Start a run to see it here.</Typography>
            ) : groups.map(({ ws, items }) => (
                <Box key={ws.id} sx={{ mb: 4 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1.5 }}>
                        <FolderIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
                        <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>{ws.label}</Typography>
                        <Typography variant="caption" color="text.secondary">{items.length}</Typography>
                    </Box>
                    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' }, gap: 2 }}>
                        {items.map((run) => <RunCard key={run.id} run={run} onOpen={onOpen} onClear={onClear} />)}
                    </Box>
                </Box>
            ))}
        </Box>
    )
}

const Dashboard = () => {
    const navigate = useNavigate()
    const location = useLocation()
    const { runId, tab } = useParams()
    const { mode } = useMode()
    const liveRuns = useRuns()
    const [dismissed, setDismissed] = useState([])

    const runs = liveRuns.filter((r) => !dismissed.includes(r.id))
    const isNew = location.pathname === '/new'

    const start = async ({ workspace, prompt, title }) => {
        const { id } = await createRun({ mode, workspace, prompt, title })
        navigate(`/run/${id}`)
    }

    return (
        <Box>
            <Landing
                runs={runs}
                onNew={() => navigate('/new')}
                onOpen={(id) => navigate(`/run/${id}`)}
                onClear={(id) => setDismissed((d) => [...d, id])}
            />

            {isNew && <NewRunWizard onClose={() => navigate('/')} onStart={start} />}
            {runId && <LiveRunWorkspace id={runId} tab={tab} onClose={() => navigate('/')} onTab={(t) => navigate(`/run/${runId}/${t}`)} />}
        </Box>
    )
}

export default Dashboard
