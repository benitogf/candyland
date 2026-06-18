import React from 'react'
import { useLocation } from 'react-router-dom'
import AppBar from '@mui/material/AppBar'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Toolbar from '@mui/material/Toolbar'
import Typography from '@mui/material/Typography'
import MenuIcon from '@mui/icons-material/Menu'

import { getCurrentSection } from './Router'

// Top bar: current section, a hamburger on mobile, and a live status chip
// (reachability + executor mode) that opens the Setup modal.
const statusChip = (system, reachable) => {
    if (!reachable) return { label: 'offline', color: 'error' }
    if (!system) return { label: 'connecting…', color: 'default' }
    return system.simulated
        ? { label: `demo · ${system.platform}`, color: 'warning' }
        : { label: `claude · ${system.platform}`, color: 'success' }
}

const Navbar = ({ drawerWidth, onMenu, system, reachable, onOpenSystem }) => {
    const location = useLocation()
    const section = getCurrentSection(location.pathname)
    const chip = statusChip(system, reachable)

    return (
        <AppBar
            position="fixed"
            elevation={0}
            sx={{
                width: { md: `calc(100% - ${drawerWidth}px)` },
                ml: { md: `${drawerWidth}px` },
                backgroundColor: 'rgba(8, 8, 10, 0.7)',
                backdropFilter: 'blur(10px)',
                borderBottom: '1px solid',
                borderColor: 'divider',
            }}
        >
            <Toolbar>
                <IconButton color="inherit" edge="start" onClick={onMenu} aria-label="open navigation" sx={{ mr: 1, display: { md: 'none' } }}>
                    <MenuIcon />
                </IconButton>
                <Typography variant="h6" noWrap sx={{ flexGrow: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {section}
                </Typography>
                <Chip label={chip.label} size="small" color={chip.color} variant="outlined" onClick={onOpenSystem} sx={{ flexShrink: 0, cursor: 'pointer' }} />
            </Toolbar>
        </AppBar>
    )
}

export default Navbar
