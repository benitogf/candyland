import React from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import Divider from '@mui/material/Divider'
import List from '@mui/material/List'
import ListItemButton from '@mui/material/ListItemButton'
import ListItemIcon from '@mui/material/ListItemIcon'
import ListItemText from '@mui/material/ListItemText'

import { navItems } from './Router'

// Left navigation rail. The product is one dashboard; opening a run overlays a
// route-driven workspace, so the rail only needs the dashboard and the spec.
const Menu = ({ onNavigate }) => {
    const location = useLocation()
    const navigate = useNavigate()

    return (
        <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
            <Box sx={{ px: 2.5, py: 2 }}>
                <Box sx={{ fontSize: 22, fontWeight: 800, letterSpacing: '-0.02em' }}>🍬 Candyland</Box>
                <Box sx={{ fontSize: 12, color: 'text.secondary', mt: 0.25 }}>solo agent orchestration</Box>
            </Box>
            <Divider />
            <List sx={{ py: 1 }}>
                {navItems.map((item) => {
                    const Icon = item.icon
                    const active = item.match(location.pathname)
                    return (
                        <ListItemButton
                            key={item.path}
                            onClick={() => { navigate(item.path); onNavigate?.() }}
                            selected={active}
                            sx={{
                                mx: 1,
                                borderRadius: 2,
                                '&.Mui-selected': {
                                    backgroundColor: 'rgba(255, 255, 255, 0.06)',
                                    color: 'primary.main',
                                    '& .MuiListItemIcon-root': { color: 'primary.main' },
                                },
                            }}
                        >
                            <ListItemIcon sx={{ minWidth: 40, color: 'text.secondary' }}><Icon /></ListItemIcon>
                            <ListItemText primary={item.label} />
                        </ListItemButton>
                    )
                })}
            </List>
            <Box sx={{ flexGrow: 1 }} />
            <Divider />
            <Box sx={{ px: 2.5, py: 1.5, fontSize: 11, color: 'text.secondary' }}>v0.1</Box>
        </Box>
    )
}

export default Menu
