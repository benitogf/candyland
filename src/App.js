import React, { useMemo, useState } from 'react'
import { BrowserRouter } from 'react-router-dom'
import CssBaseline from '@mui/material/CssBaseline'
import { ThemeProvider } from '@mui/material/styles'

import { makeTheme } from './theme'
import { ModeContext } from './mode'
import { ToastProvider } from './feedback'
import { SystemProvider } from './data/system'
import Layout from './dashboard/Layout'

// Solo, local tool: no auth gate. The active mode lives here so switching it
// recolors the whole app (the theme's primary accent swaps cyan ⇄ hot pink).
const App = () => {
    const [mode, setMode] = useState('non-developer')
    const theme = useMemo(() => makeTheme(mode), [mode])

    return (
        <ModeContext.Provider value={{ mode, setMode }}>
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
        </ModeContext.Provider>
    )
}

export default App
