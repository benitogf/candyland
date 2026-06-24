import React, { useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Drawer from '@mui/material/Drawer'
import Toolbar from '@mui/material/Toolbar'

import Menu from './Menu'
import Navbar from './Navbar'
import Footer from './Footer'
import Router from './Router'
import SystemModal from '../components/SystemModal'
import { useSystemStatus, claudeReady } from '../data/system'
import { domain } from '../config'

const drawerWidth = 248

// The dashboard shell. The nav rail is a permanent drawer on md+, and a
// temporary (hamburger) drawer on mobile. Backend reachability + setup status
// are surfaced as banners and a navbar chip → Setup modal.
const Layout = () => {
    const [mobileOpen, setMobileOpen] = useState(false)
    const [sysOpen, setSysOpen] = useState(false)
    const { system, reachable, refetch } = useSystemStatus()

    const drawerPaper = {
        width: drawerWidth, boxSizing: 'border-box',
        borderRight: '1px solid', borderColor: 'divider', backgroundColor: 'background.paper',
    }

    return (
        <Box sx={{ display: 'flex', height: '100vh' }}>
            <Navbar drawerWidth={drawerWidth} onMenu={() => setMobileOpen(true)} system={system} reachable={reachable} onOpenSystem={() => setSysOpen(true)} />

            <Drawer
                variant="temporary"
                open={mobileOpen}
                onClose={() => setMobileOpen(false)}
                ModalProps={{ keepMounted: true }}
                sx={{ display: { xs: 'block', md: 'none' }, '& .MuiDrawer-paper': drawerPaper }}
            >
                <Menu onNavigate={() => setMobileOpen(false)} />
            </Drawer>

            <Drawer
                variant="permanent"
                sx={{ display: { xs: 'none', md: 'block' }, width: drawerWidth, flexShrink: 0, '& .MuiDrawer-paper': drawerPaper }}
            >
                <Menu />
            </Drawer>

            <Box sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                <Toolbar />
                <Box component="main" sx={{ flexGrow: 1, overflowY: 'auto', overflowX: 'hidden' }}>
                    <Box sx={{ maxWidth: 1180, mx: 'auto', px: { xs: 2, sm: 4 }, py: 4 }}>
                        {!reachable && (
                            <Alert severity="error" sx={{ mb: 3 }} action={<Button color="inherit" size="small" onClick={refetch}>Retry</Button>}>
                                Can't reach the candyland server at <code>{domain}</code>. Is it running? Start it with <code>./candyland</code> (or <code>go run .</code>) — see the README "Run" section.
                            </Alert>
                        )}
                        {reachable && system && !claudeReady(system) && (
                            <Alert severity="warning" sx={{ mb: 3 }} action={<Button color="inherit" size="small" onClick={() => setSysOpen(true)}>Set up</Button>}>
                                Claude Code isn't installed — runs need it to drive the agents and open a PR. Install it to start running.
                            </Alert>
                        )}
                        <Router />
                    </Box>
                </Box>
                <Footer />
            </Box>

            <SystemModal open={sysOpen} onClose={() => setSysOpen(false)} system={system} reachable={reachable} onRetry={refetch} />
        </Box>
    )
}

export default Layout
