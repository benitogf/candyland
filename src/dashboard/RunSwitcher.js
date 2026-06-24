import React, { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import Divider from '@mui/material/Divider'
import ListItemText from '@mui/material/ListItemText'
import Menu from '@mui/material/Menu'
import MenuItem from '@mui/material/MenuItem'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import IconButton from '@mui/material/IconButton'
import SwapHorizIcon from '@mui/icons-material/SwapHoriz'
import AddIcon from '@mui/icons-material/Add'
import DashboardIcon from '@mui/icons-material/Dashboard'

import { PHASES } from '../meta/run'
import { useRuns } from '../data/ooo'
import { runLabel } from '../util'
import { ModeBadge } from '../components/StatusBits'

// Quick task switcher — jump between active runs without going back to the
// dashboard (VSCode-style). Opens on click or ⌘/Ctrl+K; lists running tasks plus
// shortcuts to start a new run or return to the dashboard. Keeps the guided flow
// flexible: you're never locked into one task.
const isRunning = (r) => r.status !== 'done'

const RunSwitcher = ({ current }) => {
    const navigate = useNavigate()
    const runs = useRuns()
    const [anchor, setAnchor] = useState(null)
    const open = Boolean(anchor)

    useEffect(() => {
        const onKey = (e) => {
            if ((e.metaKey || e.ctrlKey) && e.key && e.key.toLowerCase() === 'k') {
                e.preventDefault()
                setAnchor((a) => (a ? null : document.getElementById('run-switcher-btn')))
            }
        }
        window.addEventListener('keydown', onKey)
        return () => window.removeEventListener('keydown', onKey)
    }, [])

    const others = runs.filter((r) => isRunning(r) && r.id !== current.id)
    const go = (path) => { setAnchor(null); navigate(path) }

    return (
        <>
            <Tooltip title="Switch task (⌘K)">
                <IconButton id="run-switcher-btn" onClick={(e) => setAnchor(e.currentTarget)} aria-label="switch task" sx={{ flexShrink: 0 }}>
                    <SwapHorizIcon />
                </IconButton>
            </Tooltip>
            <Menu anchorEl={anchor} open={open} onClose={() => setAnchor(null)} slotProps={{ paper: { sx: { width: 340, maxWidth: '90vw' } } }}>
                <Typography variant="overline" color="text.secondary" sx={{ px: 2, py: 0.5, display: 'block' }}>active tasks</Typography>
                <MenuItem selected disabled sx={{ opacity: 1 }}>
                    <ListItemText primary={current.label} primaryTypographyProps={{ noWrap: true, fontWeight: 700 }} secondary="this task" />
                    <ModeBadge mode={current.mode} />
                </MenuItem>
                {others.map((r) => (
                    <MenuItem key={r.id} onClick={() => go(`/run/${r.id}`)}>
                        <ListItemText primary={runLabel(r)} primaryTypographyProps={{ noWrap: true }} secondary={PHASES[r.phase]} />
                        <ModeBadge mode={r.mode} />
                    </MenuItem>
                ))}
                {others.length === 0 && (
                    <Typography variant="body2" color="text.secondary" sx={{ px: 2, py: 1 }}>No other running tasks.</Typography>
                )}
                <Divider />
                <MenuItem onClick={() => go('/new')}><AddIcon fontSize="small" sx={{ mr: 1.5 }} /> Start a new run</MenuItem>
                <MenuItem onClick={() => go('/')}><DashboardIcon fontSize="small" sx={{ mr: 1.5 }} /> All runs (dashboard)</MenuItem>
            </Menu>
        </>
    )
}

export default RunSwitcher
