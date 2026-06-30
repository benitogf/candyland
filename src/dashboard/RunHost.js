import React from 'react'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'

import { useRun } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { beginRun, commandRun, cancelRun } from '../data/api'
import { useToast } from '../feedback'
import PlanningFlow from '../components/PlanningFlow'
import RunWorkspace from './RunWorkspace'

// Live run workspace: state comes from ooo (useRun), controls hit the backend.
// While the run is in planning, the Q&A is shown; finishing it begins the build.
export const LiveRunWorkspace = ({ id, tab, onClose, onTab }) => {
    const { reachable } = useSystemStatus()
    const toast = useToast()
    const run = useRun(id)

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

    // Show the server's reason (e.g. a 409 "not restart-able") rather than always
    // blaming reachability — the request may well have reached the server.
    const cmdFail = (e) => toast(e?.message || 'Command failed — is the candyland server reachable?')
    const planning = run.status === 'planning'
        ? <PlanningFlow
            runId={id}
            reachable={reachable}
            onComplete={(answers) => beginRun(id, answers).catch(() => toast("Couldn't begin the build — is the server reachable?"))}
            onError={() => toast("Couldn't load the planning questions — is the server reachable?")}
        />
        : null

    // Lean, flow-level control surface: Stop and Restart while running, and
    // Cancel (abandon) available in every state — including the planning Q&A,
    // where Stop has no executor to reach. No per-agent control, no resume.
    // Cancel always closes the workspace, even if the delete couldn't land, so
    // the user is never trapped on the questions.
    const controls = {
        status: run.status,
        controllable: true,
        reachable,
        stop: () => commandRun(id, 'stop').catch(cmdFail),
        restart: () => commandRun(id, 'restart').catch(cmdFail),
        cancel: () => cancelRun(id).catch(cmdFail).finally(onClose),
    }

    return <RunWorkspace run={run} controls={controls} planning={planning} tab={tab} onClose={onClose} onTab={onTab} />
}
