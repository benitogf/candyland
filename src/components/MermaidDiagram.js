import React, { useEffect, useState } from 'react'
import mermaid from 'mermaid'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'

import { mermaidTheme } from '../theme'

mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'loose',
    theme: 'base',
    themeVariables: mermaidTheme,
    flowchart: { curve: 'basis', useMaxWidth: true },
    sequence: { useMaxWidth: true },
    gantt: { useMaxWidth: true },
})

let seq = 0

// Renders a Mermaid definition to inline SVG. Each diagram gets a stable id so
// re-renders (StrictMode double-invoke, theme changes) don't collide.
const MermaidDiagram = ({ chart }) => {
    const [svg, setSvg] = useState('')
    const [error, setError] = useState('')

    useEffect(() => {
        let active = true
        const id = `candy-mmd-${seq++}`
        mermaid
            .render(id, chart.trim())
            .then(({ svg }) => {
                if (active) {
                    setSvg(svg)
                    setError('')
                }
            })
            .catch((e) => {
                if (active) setError(e?.message || String(e))
            })
        return () => {
            active = false
        }
    }, [chart])

    if (error) {
        return (
            <Box sx={{ p: 2, color: 'warning.main', fontFamily: 'monospace', fontSize: 13, whiteSpace: 'pre-wrap' }}>
                diagram error: {error}
            </Box>
        )
    }

    if (!svg) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress size={28} color="secondary" />
            </Box>
        )
    }

    return (
        <Box
            sx={{
                display: 'flex',
                justifyContent: 'center',
                py: 1,
                '& svg': { maxWidth: '100%', height: 'auto' },
            }}
            dangerouslySetInnerHTML={{ __html: svg }}
        />
    )
}

export default MermaidDiagram
