import React, { useEffect } from 'react'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'

import { useMode } from '../mode'
import { useRun } from '../data/ooo'
import { beginRun, commandRun } from '../data/api'
import PlanningFlow from '../components/PlanningFlow'
import RunWorkspace from './RunWorkspace'

// Live run workspace: state comes from ooo (useRun), controls hit the backend.
// While the run is in planning, the Q&A is shown; finishing it begins the build.
export const LiveRunWorkspace = ({ id, tab, onClose, onTab }) => {
    const { setMode } = useMode()
    const run = useRun(id)

    // Recolor the app to the run's mode once it's known.
    useEffect(() => { if (run?.mode) setMode(run.mode) }, [run?.mode, setMode])

    if (!run) {
        return (
            <Dialog fullScreen open onClose={onClose} PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none' } }}>
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

    const planning = run.status === 'planning'
        ? <PlanningFlow mode={run.mode} onComplete={(answers) => beginRun(id, answers)} />
        : null

    const controls = {
        status: run.status,
        controllable: true,
        stop: () => commandRun(id, 'stop'),
        resume: () => commandRun(id, 'resume'),
        restart: () => commandRun(id, 'restart'),
    }

    return <RunWorkspace run={run} controls={controls} planning={planning} tab={tab} onClose={onClose} onTab={onTab} />
}
