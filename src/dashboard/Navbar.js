import React from 'react'
import { useLocation } from 'react-router-dom'
import AppBar from '@mui/material/AppBar'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Toolbar from '@mui/material/Toolbar'
import Typography from '@mui/material/Typography'
import MenuIcon from '@mui/icons-material/Menu'

import { getCurrentSection } from './Router'

// Top bar showing the current section, with a hamburger to open the nav on
// mobile. The pill flags that runs here are simulated until the conductor is wired.
const Navbar = ({ drawerWidth, onMenu }) => {
    const location = useLocation()
    const section = getCurrentSection(location.pathname)

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
                <Chip label="demo data" size="small" color="secondary" variant="outlined" sx={{ flexShrink: 0 }} />
            </Toolbar>
        </AppBar>
    )
}

export default Navbar
