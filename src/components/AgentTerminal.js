import React, { useEffect, useRef } from 'react'
import Box from '@mui/material/Box'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

import { candy } from '../config'

// A real terminal (xterm.js) for an agent's raw output. In production this is
// where the process's stdout pipes; here we write the prepared lines. Handles
// arbitrary volume with native scrollback — overflow never touches the layout.
const TERM_THEME = {
    background: '#050506',
    foreground: '#a9d8c8',
    cursor: '#050506',
    selectionBackground: 'rgba(155,226,58,0.25)',
    brightBlack: '#5b5670',
}

const AgentTerminal = ({ lines, resetKey }) => {
    const hostRef = useRef(null)
    const termRef = useRef(null)
    const fitRef = useRef(null)
    const writtenRef = useRef(0)
    const keyRef = useRef(null)

    // Create the terminal once.
    useEffect(() => {
        const term = new Terminal({
            convertEol: true,
            disableStdin: true,
            cursorBlink: false,
            fontSize: 12,
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
            theme: TERM_THEME,
            scrollback: 10000,
        })
        const fit = new FitAddon()
        term.loadAddon(fit)
        term.open(hostRef.current)
        try { fit.fit() } catch { /* container not measured yet */ }
        termRef.current = term
        fitRef.current = fit
        // Fresh terminal — force the content effect to (re)write from scratch
        // (matters under StrictMode's mount/unmount/mount in dev).
        writtenRef.current = 0
        keyRef.current = null

        const ro = new ResizeObserver(() => { try { fit.fit() } catch { /* ignore */ } })
        ro.observe(hostRef.current)
        return () => { ro.disconnect(); term.dispose(); termRef.current = null }
    }, [])

    // Append-only on new output; full reset when the agent (resetKey) changes or
    // the buffer shrinks. Avoids clearing/redrawing the whole transcript on every
    // playback tick, which would flicker.
    useEffect(() => {
        const term = termRef.current
        if (!term) return
        const reset = keyRef.current !== resetKey || lines.length < writtenRef.current
        if (reset) { term.clear(); writtenRef.current = 0; keyRef.current = resetKey }
        for (let i = writtenRef.current; i < lines.length; i++) term.writeln(lines[i])
        writtenRef.current = lines.length
    }, [lines, resetKey])

    return (
        <Box
            sx={{
                height: '100%',
                borderRadius: 2,
                overflow: 'hidden',
                border: '1px solid',
                borderColor: 'divider',
                backgroundColor: '#050506',
                '& .xterm': { padding: '10px' },
                '& .xterm-viewport': { backgroundColor: `${candy.bgDark} !important` },
            }}
        >
            <Box ref={hostRef} sx={{ height: '100%' }} />
        </Box>
    )
}

export default AgentTerminal
