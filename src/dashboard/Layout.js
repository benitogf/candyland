import React, { useState } from 'react'
import Box from '@mui/material/Box'
import Drawer from '@mui/material/Drawer'
import Toolbar from '@mui/material/Toolbar'

import Menu from './Menu'
import Navbar from './Navbar'
import Footer from './Footer'
import Router from './Router'

const drawerWidth = 248

// The dashboard shell. The nav rail is a permanent drawer on md+, and a
// temporary (hamburger) drawer on mobile — so small screens get full width and
// never overflow horizontally.
const Layout = () => {
    const [mobileOpen, setMobileOpen] = useState(false)

    const drawerPaper = {
        width: drawerWidth, boxSizing: 'border-box',
        borderRight: '1px solid', borderColor: 'divider', backgroundColor: 'background.paper',
    }

    return (
        <Box sx={{ display: 'flex', height: '100vh' }}>
            <Navbar drawerWidth={drawerWidth} onMenu={() => setMobileOpen(true)} />

            {/* Mobile: temporary overlay drawer */}
            <Drawer
                variant="temporary"
                open={mobileOpen}
                onClose={() => setMobileOpen(false)}
                ModalProps={{ keepMounted: true }}
                sx={{ display: { xs: 'block', md: 'none' }, '& .MuiDrawer-paper': drawerPaper }}
            >
                <Menu onNavigate={() => setMobileOpen(false)} />
            </Drawer>

            {/* Desktop: permanent drawer in the flex flow */}
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
                        <Router />
                    </Box>
                </Box>
                <Footer />
            </Box>
        </Box>
    )
}

export default Layout
