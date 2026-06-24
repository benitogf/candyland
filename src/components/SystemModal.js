import React from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline'
import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import CloseIcon from '@mui/icons-material/Close'

import { candy, domain } from '../config'
import { useToast } from '../feedback'
import { claudeReady } from '../data/system'

const CommandBlock = ({ cmd }) => {
    const toast = useToast()
    // Confirm the copy, and never let a clipboard rejection (denied permission,
    // insecure context) fail silently — the command stays on-screen to copy by hand.
    const copy = () => {
        if (!navigator.clipboard) {
            toast('Copy unavailable here — select the command and copy it manually')
            return
        }
        navigator.clipboard.writeText(cmd)
            .then(() => toast('Copied to clipboard', 'success'))
            .catch(() => toast("Couldn't copy — select the command and copy it manually"))
    }
    return (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 0.5, p: 1, borderRadius: 1, backgroundColor: candy.bgInk, border: '1px solid', borderColor: 'divider' }}>
            <Typography variant="caption" sx={{ fontFamily: 'monospace', color: candy.mint, flexGrow: 1, wordBreak: 'break-all' }}>{cmd}</Typography>
            <Tooltip title="Copy">
                <IconButton size="small" onClick={copy} aria-label="copy command"><ContentCopyIcon sx={{ fontSize: 15 }} /></IconButton>
            </Tooltip>
        </Box>
    )
}

const DepRow = ({ dep }) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.25, py: 1, borderBottom: '1px solid', borderColor: 'divider' }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            {dep.installed
                ? <CheckCircleIcon sx={{ fontSize: 18, color: '#7bdc6a' }} />
                : <ErrorOutlineIcon sx={{ fontSize: 18, color: candy.lemon }} />}
            <Typography variant="body2" sx={{ fontWeight: 700 }}>{dep.name}</Typography>
            <Typography variant="caption" color="text.secondary" noWrap sx={{ flexGrow: 1, minWidth: 0 }}>
                {dep.installed ? (dep.version || 'installed') : 'not installed'}
            </Typography>
        </Box>
        <Typography variant="caption" color="text.secondary">{dep.why}</Typography>
        {!dep.installed && dep.install && <CommandBlock cmd={dep.install} />}
    </Box>
)

// Setup & status: the detected platform, dependency state, and per-platform
// install commands. Everything the user needs to get a machine ready to run real
// agents, with copy-paste commands.
const SystemModal = ({ open, onClose, system, reachable, onRetry }) => (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth PaperProps={{ sx: { backgroundImage: 'none' } }}>
        <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1, pr: 1.5 }}>
            <Box sx={{ flexGrow: 1 }}>
                <Typography variant="h6" sx={{ fontWeight: 800 }}>Setup & status</Typography>
                <Typography variant="caption" color="text.secondary">Where candyland runs, what it needs, and how to enable real runs.</Typography>
            </Box>
            <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
        </DialogTitle>
        <DialogContent dividers sx={{ borderColor: 'divider' }}>
            {!reachable ? (
                <Alert severity="error" variant="outlined" action={<Chip label="Retry" size="small" onClick={onRetry} sx={{ cursor: 'pointer' }} />}>
                    Can't reach the candyland server ({domain}). Start it, then retry — see the README "Run" section.
                </Alert>
            ) : !system ? (
                <Typography variant="body2" color="text.secondary">Loading…</Typography>
            ) : (
                <>
                    <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', mb: 2 }}>
                        <Chip size="small" variant="outlined" label={`platform · ${system.platform} (${system.arch})`} />
                        <Chip size="small" variant="outlined" label={`candyland · ${system.version}`} />
                        <Chip size="small" variant="outlined" color={claudeReady(system) ? 'success' : 'warning'} label={claudeReady(system) ? 'claude · ready' : 'claude · not installed'} />
                    </Box>

                    {!claudeReady(system) && (
                        <Alert severity="warning" variant="outlined" sx={{ mb: 2 }}>
                            Claude Code isn't installed, so runs can't start — there's no demo mode. Install it (below) to drive real agents and open PRs.
                        </Alert>
                    )}

                    <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 0.5 }}>dependencies</Typography>
                    {system.deps?.map((d) => <DepRow key={d.name} dep={d} />)}

                    {system.recommendations?.length > 0 && (
                        <Box sx={{ mt: 2 }}>
                            <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 0.5 }}>recommendations</Typography>
                            {system.recommendations.map((r, i) => (
                                <Typography key={i} variant="body2" color="text.secondary" sx={{ mb: 0.5 }}>• {r}</Typography>
                            ))}
                        </Box>
                    )}
                </>
            )}
        </DialogContent>
    </Dialog>
)

export default SystemModal
