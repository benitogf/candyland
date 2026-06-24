import React, { useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Checkbox from '@mui/material/Checkbox'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogContentText from '@mui/material/DialogContentText'
import DialogTitle from '@mui/material/DialogTitle'
import Divider from '@mui/material/Divider'
import FormControlLabel from '@mui/material/FormControlLabel'
import IconButton from '@mui/material/IconButton'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import AddIcon from '@mui/icons-material/Add'
import CloseIcon from '@mui/icons-material/Close'
import FolderIcon from '@mui/icons-material/Folder'
import FolderOpenIcon from '@mui/icons-material/FolderOpen'
import CreateNewFolderIcon from '@mui/icons-material/CreateNewFolder'
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline'

import { useActiveWorkspaces } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { useFolderStatuses, folderIssue } from '../data/fs'
import { createWorkspace, deleteWorkspace, cancelRun } from '../data/api'
import { useToast } from '../feedback'
import FolderPicker from './FolderPicker'

// Manage the saved folder sets, as a modal opened from the dashboard. Workspaces
// are persisted in the backend (ooo) — created/deleted via REST, read live. Each
// folder is a real, absolute path the backend can read+write; the card checks
// them live against the filesystem and flags any that have gone missing.

const WorkspaceCard = ({ ws, onDelete, disabled }) => {
    const statuses = useFolderStatuses(ws.folders)
    const issues = ws.folders.filter((f) => folderIssue(statuses[f])).length
    return (
        <Card sx={{ borderColor: issues ? 'warning.main' : undefined }}>
            <CardContent>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1 }}>{ws.label}</Typography>
                    {issues > 0 && <Chip size="small" color="warning" variant="outlined" icon={<ErrorOutlineIcon sx={{ fontSize: 14 }} />} label={`${issues} unavailable`} sx={{ height: 20, fontSize: 11 }} />}
                    <Chip size="small" variant="outlined" label={`${ws.folders.length} folder${ws.folders.length === 1 ? '' : 's'}`} sx={{ height: 20, fontSize: 11 }} />
                    <IconButton size="small" onClick={() => onDelete(ws.id)} disabled={disabled} aria-label="delete workspace"><CloseIcon sx={{ fontSize: 16 }} /></IconButton>
                </Box>
                <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
                    {ws.folders.map((f) => {
                        const issue = folderIssue(statuses[f])
                        return (
                            <Box key={f} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                {issue
                                    ? <Tooltip title={issue}><ErrorOutlineIcon sx={{ fontSize: 15, color: 'warning.main' }} /></Tooltip>
                                    : <FolderIcon sx={{ fontSize: 15, color: 'text.secondary' }} />}
                                <Typography variant="caption" color={issue ? 'warning.main' : 'text.secondary'} sx={{ fontFamily: 'monospace' }}>{f}</Typography>
                                {issue && <Typography variant="caption" color="warning.main" sx={{ flexShrink: 0 }}>· {issue}</Typography>}
                            </Box>
                        )
                    })}
                </Box>
            </CardContent>
        </Card>
    )
}

const NewWorkspaceForm = ({ onCreate, disabled }) => {
    const [name, setName] = useState('')
    const [folders, setFolders] = useState([])
    const [pickerOpen, setPickerOpen] = useState(false)

    const addFolder = (f) => { if (f && !folders.includes(f)) setFolders([...folders, f]) }
    const create = () => {
        if (!name.trim() || folders.length === 0) return
        // The server derives the (ooo-key-safe) id from the label and validates the
        // folders; we send just name + the picked absolute paths.
        onCreate({ label: name.trim(), folders })
        setName(''); setFolders([])
    }

    return (
        <Card sx={{ mb: 3, borderColor: 'primary.main' }}>
            <CardContent sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <CreateNewFolderIcon color="primary" />
                    <Typography variant="subtitle1" sx={{ fontWeight: 700 }}>New workspace</Typography>
                </Box>
                <TextField size="small" label="Name" placeholder="e.g. Full stack" value={name} onChange={(e) => setName(e.target.value)} sx={{ maxWidth: 360 }} />
                <Box>
                    <Button variant="outlined" startIcon={<FolderOpenIcon />} disabled={disabled} onClick={() => setPickerOpen(true)}>Browse for a folder</Button>
                    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, mt: 1.5 }}>
                        {folders.length === 0
                            ? <Typography variant="caption" color="text.secondary">No folders yet — browse to add at least one. The first folder is the repo the run branches and opens its PR in.</Typography>
                            : folders.map((f) => (
                                <Box key={f} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                    <FolderIcon sx={{ fontSize: 15, color: 'text.secondary' }} />
                                    <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace', flexGrow: 1, wordBreak: 'break-all' }}>{f}</Typography>
                                    <IconButton size="small" aria-label="remove folder" onClick={() => setFolders(folders.filter((x) => x !== f))}><CloseIcon sx={{ fontSize: 14 }} /></IconButton>
                                </Box>
                            ))}
                    </Box>
                </Box>
                <Box>
                    <Button variant="contained" startIcon={<AddIcon />} disabled={disabled || !name.trim() || folders.length === 0} onClick={create}>
                        Create workspace
                    </Button>
                </Box>
            </CardContent>
            <FolderPicker open={pickerOpen} onClose={() => setPickerOpen(false)} onPick={addFolder} />
        </Card>
    )
}

// When a delete is blocked by active runs, confirm cancelling EACH one before
// proceeding (per-task acknowledgement), then cancel them and retry the delete.
const BlockingTasksDialog = ({ state, onClose, onConfirm }) => {
    const [checked, setChecked] = useState({})
    const [busy, setBusy] = useState(false)
    const runs = state?.runs || []

    // Reset the per-task checkboxes whenever a new blocking set is shown.
    useEffect(() => { setChecked({}); setBusy(false) }, [state])

    const allConfirmed = runs.length > 0 && runs.every((r) => checked[r.id])
    const confirm = async () => {
        setBusy(true)
        try { await onConfirm(state.wsId, runs) } finally { setBusy(false) }
    }

    return (
        <Dialog open={!!state} onClose={busy ? undefined : onClose} maxWidth="xs" fullWidth PaperProps={{ sx: { backgroundImage: 'none' } }}>
            <DialogTitle sx={{ fontWeight: 800 }}>Tasks are using this workspace</DialogTitle>
            <DialogContent dividers sx={{ borderColor: 'divider' }}>
                <DialogContentText sx={{ mb: 1.5 }}>
                    Deleting it will cancel {runs.length === 1 ? 'this task' : `these ${runs.length} tasks`}. Confirm each to continue — the workspace is then hidden (its folders stay on disk).
                </DialogContentText>
                <Box sx={{ display: 'flex', flexDirection: 'column' }}>
                    {runs.map((r) => (
                        <FormControlLabel
                            key={r.id}
                            control={<Checkbox size="small" checked={!!checked[r.id]} onChange={(e) => setChecked((c) => ({ ...c, [r.id]: e.target.checked }))} />}
                            label={
                                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                    <Typography variant="body2" sx={{ fontWeight: 600 }}>{r.title || r.id}</Typography>
                                    <Chip size="small" variant="outlined" label={r.status} sx={{ height: 18, fontSize: 10 }} />
                                </Box>
                            }
                        />
                    ))}
                </Box>
            </DialogContent>
            <DialogActions sx={{ px: 2, py: 1.5 }}>
                <Button color="inherit" onClick={onClose} disabled={busy}>Keep workspace</Button>
                <Button color="error" variant="contained" onClick={confirm} disabled={!allConfirmed || busy}>
                    Cancel {runs.length === 1 ? 'task' : 'tasks'} &amp; delete
                </Button>
            </DialogActions>
        </Dialog>
    )
}

const WorkspacesModal = ({ open, onClose }) => {
    const list = useActiveWorkspaces()
    const { reachable } = useSystemStatus()
    const toast = useToast()
    const [blocking, setBlocking] = useState(null) // { wsId, runs } when a delete is blocked

    // Delete a workspace; on a 409 (active runs reference it), surface the blockers
    // so the user can confirm cancelling them, then retry.
    const handleDelete = (id) =>
        deleteWorkspace(id).catch((e) => {
            if (e.blocking && e.blocking.length) setBlocking({ wsId: id, runs: e.blocking })
            else toast(e.message || "Couldn't delete the workspace.")
        })

    const cancelThenDelete = async (wsId, runs) => {
        try {
            for (const r of runs) await cancelRun(r.id)
            await deleteWorkspace(wsId)
            setBlocking(null)
        } catch (e) {
            // Still blocked (a new run started meanwhile) → refresh the list; else report.
            if (e.blocking && e.blocking.length) setBlocking({ wsId, runs: e.blocking })
            else { setBlocking(null); toast(e.message || "Couldn't delete the workspace.") }
        }
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth PaperProps={{ sx: { backgroundImage: 'none' } }}>
            <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1, pr: 1.5 }}>
                <Box sx={{ flexGrow: 1 }}>
                    <Typography variant="h6" sx={{ fontWeight: 800 }}>Workspaces</Typography>
                    <Typography variant="caption" color="text.secondary">A named set of folders a run can touch — saved so you pick it once.</Typography>
                </Box>
                <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
            </DialogTitle>
            <DialogContent dividers sx={{ borderColor: 'divider' }}>
                {!reachable && (
                    <Alert severity="error" variant="outlined" sx={{ mb: 2 }}>
                        Server unreachable — start <code>./candyland</code> to add or remove workspaces.
                    </Alert>
                )}
                <NewWorkspaceForm disabled={!reachable} onCreate={(ws) => createWorkspace(ws).catch((e) => toast(e.message || "Couldn't create the workspace — is the server reachable?"))} />
                <Divider sx={{ mb: 3 }} />
                <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1.5 }}>saved · {list.length}</Typography>
                {list.length === 0 ? (
                    <Typography variant="body2" color="text.secondary">No saved workspaces yet — create one above.</Typography>
                ) : (
                    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)' }, gap: 2 }}>
                        {list.map((ws) => <WorkspaceCard key={ws.id} ws={ws} disabled={!reachable} onDelete={handleDelete} />)}
                    </Box>
                )}
            </DialogContent>
            <BlockingTasksDialog state={blocking} onClose={() => setBlocking(null)} onConfirm={cancelThenDelete} />
        </Dialog>
    )
}

export default WorkspacesModal
