import React from 'react'
import { Route, Routes, Navigate } from 'react-router-dom'
import DashboardIcon from '@mui/icons-material/Dashboard'
import HistoryIcon from '@mui/icons-material/History'
import MenuBookIcon from '@mui/icons-material/MenuBook'

import Dashboard from '../pages/Dashboard'
import Tasks from '../pages/Tasks'
import HowItWorks from '../pages/HowItWorks'
import WorkDetail from './WorkDetail'

// The whole product is one dashboard. Opening a run is a route-driven full-screen
// view that overlays the dashboard (/run/:id/:tab). Tasks is the full run
// history; spec is the docs. Runs are launched by detritus over REST — the
// dashboard only observes them.
export const navItems = [
    { path: '/', label: 'Dashboard', icon: DashboardIcon, match: (p) => p === '/' || p.startsWith('/run') },
    { path: '/tasks', label: 'Work', icon: HistoryIcon, match: (p) => p.startsWith('/tasks') || p.startsWith('/quest') || p.startsWith('/campaign') },
    { path: '/how-it-works', label: 'How it works', icon: MenuBookIcon, match: (p) => p.startsWith('/how-it-works') },
]

export const getCurrentSection = (pathname) => {
    if (pathname.startsWith('/how-it-works')) return 'How it works'
    if (pathname.startsWith('/tasks')) return 'Work'
    if (pathname.startsWith('/run/')) return 'Run'
    if (pathname.startsWith('/quest/')) return 'Quest'
    if (pathname.startsWith('/campaign/')) return 'Campaign'
    return 'Dashboard'
}

// The work/history section is one route (/tasks) that pivots Runs ↔ Quests ↔
// Campaigns via query params. Opening a quest/campaign overlays the work section
// (it renders behind, so the filtered list and pivot survive the overlay close);
// a run keeps its existing overlay-on-Dashboard behaviour.
const Router = () => (
    <Routes>
        <Route path="/how-it-works" element={<HowItWorks />} />
        <Route path="/tasks" element={<Tasks />} />
        <Route path="/quest/:id" element={<><Tasks /><WorkDetail kind="quest" /></>} />
        <Route path="/campaign/:id" element={<><Tasks /><WorkDetail kind="campaign" /></>} />
        <Route path="/" element={<Dashboard />} />
        <Route path="/run/:runId" element={<Dashboard />} />
        <Route path="/run/:runId/:tab" element={<Dashboard />} />
        <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
)

export default Router
