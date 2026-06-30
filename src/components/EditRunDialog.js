import React, { useEffect, useState } from 'react'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch'

import { editRun } from '../data/api'
import { useToast } from '../feedback'
import CommandInput from './CommandInput'

// Edit a finished task IN PLACE: change the request (and title), save, and the
// run resets to planning — the questions regenerate from the new prompt and it
// re-runs in the same folders. Distinct from Restart, which re-runs as-is.
const EditRunDialog = ({ run, open, onClose }) => {
    const toast = useToast()
    const [prompt, setPrompt] = useState(run.prompt)
    const [title, setTitle] = useState(run.title || '')

    // Re-seed from the run each time the dialog opens (or the run changes).
    useEffect(() => {
        if (!open) return
        setPrompt(run.prompt); setTitle(run.title || '')
    }, [open, run.id]) // eslint-disable-line react-hooks/exhaustive-deps

    const canSave = prompt.trim().length > 0
    const save = () => {
        if (!canSave) return
        // The run keeps its folders; resend them so /edit's folder requirement is met.
        editRun(run.id, { folders: run.folders, prompt: prompt.trim(), title: title.trim() })
            .then(onClose)
            .catch((e) => toast(e.message || "Couldn't save the changes."))
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth PaperProps={{ sx: { backgroundImage: 'none' } }}>
            <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1, pr: 1.5 }}>
                <Box sx={{ flexGrow: 1 }}>
                    <Typography variant="h6" sx={{ fontWeight: 800 }}>Edit task</Typography>
                    <Typography variant="caption" color="text.secondary">Changing it re-asks the planning questions, then re-runs this same task.</Typography>
                </Box>
                <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
            </DialogTitle>
            <DialogContent dividers sx={{ borderColor: 'divider', display: 'flex', flexDirection: 'column', gap: 2 }}>
                <Box>
                    <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 0.5 }}>what you want done</Typography>
                    <CommandInput fullWidth multiline minRows={4} placeholder="Describe the change…" value={prompt} onChange={setPrompt} />
                </Box>

                <TextField size="small" label="Title (optional)" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="A short label for this task" />
            </DialogContent>
            <DialogActions sx={{ px: 2, py: 1.5 }}>
                <Button color="inherit" onClick={onClose}>Cancel</Button>
                <Button variant="contained" startIcon={<RocketLaunchIcon />} disabled={!canSave} onClick={save}>Save &amp; re-plan</Button>
            </DialogActions>
        </Dialog>
    )
}

export default EditRunDialog
