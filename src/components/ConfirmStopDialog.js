import React from 'react'
import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogContentText from '@mui/material/DialogContentText'
import DialogTitle from '@mui/material/DialogTitle'
import StopCircleIcon from '@mui/icons-material/StopCircle'

// Stop is terminal and irreversible, and it CASCADES to children — so it always
// goes through this confirmation. `scope` names exactly what will be stopped
// (e.g. "this campaign and its 3 quests / 7 runs") so the user sees the blast
// radius before confirming. Callers pass what/scope; the dialog owns nothing but
// the confirm gate.
const ConfirmStopDialog = ({ open, what, scope, onConfirm, onCancel }) => (
    <Dialog open={open} onClose={onCancel} aria-label={`Stop ${what}?`}>
        <DialogTitle>Stop {what}?</DialogTitle>
        <DialogContent>
            <DialogContentText>
                This cannot be undone. Stopping {scope} halts the work immediately.
            </DialogContentText>
        </DialogContent>
        <DialogActions>
            <Button color="inherit" onClick={onCancel}>Keep running</Button>
            <Button color="error" variant="contained" startIcon={<StopCircleIcon />} onClick={onConfirm}>Stop</Button>
        </DialogActions>
    </Dialog>
)

export default ConfirmStopDialog
