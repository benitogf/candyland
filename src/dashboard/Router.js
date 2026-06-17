import React from 'react'
import { Route, Routes, Navigate } from 'react-router-dom'
import DashboardIcon from '@mui/icons-material/Dashboard'
import MenuBookIcon from '@mui/icons-material/MenuBook'

import Dashboard from '../pages/Dashboard'
import HowItWorks from '../pages/HowItWorks'

// The whole product is one dashboard. Opening a run is a route-driven full-screen
// workspace that overlays the dashboard (/run/:id/:tab); workspaces are managed
// in a modal from the dashboard. The nav rail stays small: the dashboard + spec.
export const navItems = [
    { path: '/', label: 'Dashboard', icon: DashboardIcon, match: (p) => p === '/' || p.startsWith('/run') },
    { path: '/how-it-works', label: 'How it works', icon: MenuBookIcon, match: (p) => p.startsWith('/how-it-works') },
]

export const getCurrentSection = (pathname) => {
    if (pathname.startsWith('/how-it-works')) return 'How it works'
    if (pathname.startsWith('/run/')) return 'Run'
    return 'Dashboard'
}

const Router = () => (
    <Routes>
        <Route path="/how-it-works" element={<HowItWorks />} />
        <Route path="/" element={<Dashboard />} />
        <Route path="/new" element={<Dashboard />} />
        <Route path="/run/:runId" element={<Dashboard />} />
        <Route path="/run/:runId/:tab" element={<Dashboard />} />
        <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
)

export default Router
