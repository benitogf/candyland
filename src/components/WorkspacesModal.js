import React, { useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import Divider from '@mui/material/Divider'
import IconButton from '@mui/material/IconButton'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import AddIcon from '@mui/icons-material/Add'
import CloseIcon from '@mui/icons-material/Close'
import FolderIcon from '@mui/icons-material/Folder'
import CreateNewFolderIcon from '@mui/icons-material/CreateNewFolder'

import { useWorkspaces } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { createWorkspace, deleteWorkspace } from '../data/api'
import { useToast } from '../feedback'
import { slug } from '../util'

// Manage the saved folder sets, as a modal opened from the dashboard. Workspaces
// are persisted in the backend (ooo) — created/deleted via REST, read live.

const WorkspaceCard = ({ ws, onDelete, disabled }) => (
    <Card>
        <CardContent>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1 }}>{ws.label}</Typography>
                <Chip size="small" variant="outlined" label={`${ws.folders.length} folder${ws.folders.length === 1 ? '' : 's'}`} sx={{ height: 20, fontSize: 11 }} />
                <IconButton size="small" onClick={() => onDelete(ws.id)} disabled={disabled} aria-label="delete workspace"><CloseIcon sx={{ fontSize: 16 }} /></IconButton>
            </Box>
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
                {ws.folders.map((f) => (
                    <Box key={f} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                        <FolderIcon sx={{ fontSize: 15, color: 'text.secondary' }} />
                        <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace' }}>{f}</Typography>
                    </Box>
                ))}
            </Box>
        </CardContent>
    </Card>
)

const NewWorkspaceForm = ({ onCreate, disabled }) => {
    const [name, setName] = useState('')
    const [folder, setFolder] = useState('')
    const [folders, setFolders] = useState([])

    const addFolder = () => {
        const f = folder.trim()
        if (f && !folders.includes(f)) { setFolders([...folders, f]); setFolder('') }
    }
    const create = () => {
        if (!name.trim() || folders.length === 0) return
        // Slug the id with the same rule the server uses (src/util.js slug ≡ server
        // slugify), so a name with punctuation produces a valid, matching ooo key.
        onCreate({ id: slug(name), label: name.trim(), folders })
        setName(''); setFolders([]); setFolder('')
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
                    <Box sx={{ display: 'flex', gap: 1, alignItems: 'flex-start', flexWrap: 'wrap' }}>
                        <TextField
                            size="small" label="Add a folder" placeholder="~/src/acme/web" value={folder}
                            onChange={(e) => setFolder(e.target.value)}
                            onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addFolder() } }}
                            sx={{ flexGrow: 1, minWidth: 280 }}
                        />
                        <Button variant="outlined" onClick={addFolder} sx={{ mt: 0.25 }}>Add folder</Button>
                    </Box>
                    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, mt: 1.5 }}>
                        {folders.length === 0
                            ? <Typography variant="caption" color="text.secondary">No folders yet — a workspace needs at least one.</Typography>
                            : folders.map((f) => (
                                <Box key={f} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                    <FolderIcon sx={{ fontSize: 15, color: 'text.secondary' }} />
                                    <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace', flexGrow: 1 }}>{f}</Typography>
                                    <IconButton size="small" onClick={() => setFolders(folders.filter((x) => x !== f))}><CloseIcon sx={{ fontSize: 14 }} /></IconButton>
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
        </Card>
    )
}

const WorkspacesModal = ({ open, onClose }) => {
    const list = useWorkspaces()
    const { reachable } = useSystemStatus()
    const toast = useToast()

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
                <NewWorkspaceForm disabled={!reachable} onCreate={(ws) => createWorkspace(ws).catch(() => toast("Couldn't create the workspace — is the server reachable?"))} />
                <Divider sx={{ mb: 3 }} />
                <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1.5 }}>saved · {list.length}</Typography>
                {list.length === 0 ? (
                    <Typography variant="body2" color="text.secondary">No saved workspaces yet — create one above.</Typography>
                ) : (
                    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)' }, gap: 2 }}>
                        {list.map((ws) => <WorkspaceCard key={ws.id} ws={ws} disabled={!reachable} onDelete={(id) => deleteWorkspace(id).catch(() => toast("Couldn't delete the workspace."))} />)}
                    </Box>
                )}
            </DialogContent>
        </Dialog>
    )
}

export default WorkspacesModal
