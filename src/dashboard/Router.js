import React from 'react'
import { Route, Routes, Navigate } from 'react-router-dom'
import DashboardIcon from '@mui/icons-material/Dashboard'
import HistoryIcon from '@mui/icons-material/History'
import MenuBookIcon from '@mui/icons-material/MenuBook'

import Dashboard from '../pages/Dashboard'
import Tasks from '../pages/Tasks'
import HowItWorks from '../pages/HowItWorks'

// The whole product is one dashboard. Opening a run is a route-driven full-screen
// workspace that overlays the dashboard (/run/:id/:tab); workspaces are managed
// in a modal from the dashboard. Tasks is the full run history; spec is the docs.
export const navItems = [
    { path: '/', label: 'Dashboard', icon: DashboardIcon, match: (p) => p === '/' || p.startsWith('/run') },
    { path: '/tasks', label: 'Tasks', icon: HistoryIcon, match: (p) => p.startsWith('/tasks') },
    { path: '/how-it-works', label: 'How it works', icon: MenuBookIcon, match: (p) => p.startsWith('/how-it-works') },
]

export const getCurrentSection = (pathname) => {
    if (pathname.startsWith('/how-it-works')) return 'How it works'
    if (pathname.startsWith('/tasks')) return 'Tasks'
    if (pathname.startsWith('/run/')) return 'Run'
    return 'Dashboard'
}

const Router = () => (
    <Routes>
        <Route path="/how-it-works" element={<HowItWorks />} />
        <Route path="/tasks" element={<Tasks />} />
        <Route path="/" element={<Dashboard />} />
        <Route path="/new" element={<Dashboard />} />
        <Route path="/run/:runId" element={<Dashboard />} />
        <Route path="/run/:runId/:tab" element={<Dashboard />} />
        <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
)

export default Router
