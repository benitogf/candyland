import React, { useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import List from '@mui/material/List'
import ListItemButton from '@mui/material/ListItemButton'
import ListItemIcon from '@mui/material/ListItemIcon'
import ListItemText from '@mui/material/ListItemText'
import Typography from '@mui/material/Typography'
import ArrowUpwardIcon from '@mui/icons-material/ArrowUpward'
import CloseIcon from '@mui/icons-material/Close'
import FolderIcon from '@mui/icons-material/Folder'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'

import { listDir } from '../data/api'

// Browse the BACKEND's filesystem (the process that will run the agents) and pick
// a real directory — so you never type a path, and what you pick is exactly what
// the backend can see. Shows the resolved absolute path, which also answers
// "where does ~ land?" (it lands in the backend's home).
const FolderPicker = ({ open, onClose, onPick }) => {
    const [path, setPath] = useState('')
    const [data, setData] = useState(null)
    const [error, setError] = useState('')
    const [loading, setLoading] = useState(false)

    // Start each opening from the backend's home.
    useEffect(() => { if (open) setPath('') }, [open])

    useEffect(() => {
        if (!open) return undefined
        let live = true
        setLoading(true); setError('')
        listDir(path)
            .then((d) => { if (live) { setData(d); setLoading(false) } })
            .catch((e) => { if (live) { setError(e.message); setLoading(false) } })
        return () => { live = false }
    }, [open, path])

    const here = data?.path || ''

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth PaperProps={{ sx: { backgroundImage: 'none' } }}>
            <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1, pr: 1.5 }}>
                <Box sx={{ flexGrow: 1 }}>
                    <Typography variant="h6" sx={{ fontWeight: 800 }}>Pick a folder</Typography>
                    <Typography variant="caption" color="text.secondary">On the machine running candyland. The path you pick is absolute.</Typography>
                </Box>
                <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
            </DialogTitle>
            <DialogContent dividers sx={{ borderColor: 'divider' }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <IconButton size="small" aria-label="up one folder" disabled={!data?.parent} onClick={() => setPath(data.parent)}>
                        <ArrowUpwardIcon fontSize="small" />
                    </IconButton>
                    <Typography variant="caption" sx={{ fontFamily: 'monospace', wordBreak: 'break-all', color: 'text.secondary', flexGrow: 1 }}>{here || '…'}</Typography>
                </Box>

                {error && <Alert severity="error" variant="outlined" sx={{ mb: 1 }}>{error}</Alert>}

                {loading ? (
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, py: 3, justifyContent: 'center' }}>
                        <CircularProgress size={18} /><Typography variant="body2" color="text.secondary">Reading…</Typography>
                    </Box>
                ) : (
                    <List dense sx={{ maxHeight: 320, overflowY: 'auto' }}>
                        {(data?.entries || []).length === 0
                            ? <Typography variant="body2" color="text.secondary" sx={{ px: 1, py: 2 }}>No sub-folders here. You can still add this folder.</Typography>
                            : data.entries.map((e) => (
                                <ListItemButton key={e.path} onClick={() => setPath(e.path)}>
                                    <ListItemIcon sx={{ minWidth: 34 }}><FolderIcon fontSize="small" sx={{ color: 'text.secondary' }} /></ListItemIcon>
                                    <ListItemText primary={e.name} />
                                </ListItemButton>
                            ))}
                    </List>
                )}
            </DialogContent>
            <DialogActions sx={{ px: 2, py: 1.5 }}>
                <Button color="inherit" onClick={onClose}>Cancel</Button>
                <Button variant="contained" startIcon={<CheckCircleIcon />} disabled={!here} onClick={() => { onPick(here); onClose() }}>
                    Add this folder
                </Button>
            </DialogActions>
        </Dialog>
    )
}

export default FolderPicker
