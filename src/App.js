import React, { useMemo } from 'react'
import { BrowserRouter } from 'react-router-dom'
import CssBaseline from '@mui/material/CssBaseline'
import { ThemeProvider } from '@mui/material/styles'

import { makeTheme } from './theme'
import { ToastProvider } from './feedback'
import { SystemProvider } from './data/system'
import Layout from './dashboard/Layout'

// Solo, local tool: no auth gate.
const App = () => {
    const theme = useMemo(() => makeTheme(), [])

    return (
        <ThemeProvider theme={theme}>
            <CssBaseline />
            <ToastProvider>
                <SystemProvider>
                    <BrowserRouter>
                        <Layout />
                    </BrowserRouter>
                </SystemProvider>
            </ToastProvider>
        </ThemeProvider>
    )
}

export default App
