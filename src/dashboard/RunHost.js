import React, { useEffect } from 'react'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'

import { useMode } from '../mode'
import { useRun } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { beginRun, commandRun } from '../data/api'
import { useToast } from '../feedback'
import PlanningFlow from '../components/PlanningFlow'
import RunWorkspace from './RunWorkspace'

// Live run workspace: state comes from ooo (useRun), controls hit the backend.
// While the run is in planning, the Q&A is shown; finishing it begins the build.
export const LiveRunWorkspace = ({ id, tab, onClose, onTab }) => {
    const { setMode } = useMode()
    const { reachable } = useSystemStatus()
    const toast = useToast()
    const run = useRun(id)

    // Recolor the app to the run's mode once it's known.
    useEffect(() => { if (run?.mode) setMode(run.mode) }, [run?.mode, setMode])

    if (!run) {
        return (
            <Dialog fullScreen open onClose={onClose} aria-label="Connecting to the run" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none' } }}>
                <Box sx={{ display: 'flex', justifyContent: 'flex-end', p: 1 }}>
                    <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
                </Box>
                <Box sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2 }}>
                    <CircularProgress />
                    <Typography variant="body2" color="text.secondary">Connecting to the run…</Typography>
                </Box>
            </Dialog>
        )
    }

    const cmdFail = () => toast('Command failed — is the candyland server reachable?')
    const planning = run.status === 'planning'
        ? <PlanningFlow
            mode={run.mode}
            reachable={reachable}
            onComplete={(answers) => beginRun(id, answers).catch(() => toast("Couldn't begin the build — is the server reachable?"))}
            onError={() => toast("Couldn't load the planning questions — is the server reachable?")}
        />
        : null

    // Lean, flow-level control surface: Stop and Restart only (no per-agent
    // control, no resume — see RunControls). Disabled when the server is
    // unreachable, since a command can't land.
    const controls = {
        status: run.status,
        controllable: true,
        reachable,
        stop: () => commandRun(id, 'stop').catch(cmdFail),
        restart: () => commandRun(id, 'restart').catch(cmdFail),
    }

    return <RunWorkspace run={run} controls={controls} planning={planning} tab={tab} onClose={onClose} onTab={onTab} />
}
