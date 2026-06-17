import React, { useRef, useState } from 'react'
import Box from '@mui/material/Box'
import MenuItem from '@mui/material/MenuItem'
import MenuList from '@mui/material/MenuList'
import Paper from '@mui/material/Paper'
import Popper from '@mui/material/Popper'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'

import { DETRITUS_COMMANDS } from '../data/commands'

// A text input with detritus slash-command autocomplete. When the token at the
// caret starts with "/", a popup lists matching commands; ↑/↓ move, Enter/Tab
// insert, Esc dismisses, click inserts. Works for single-line and multiline.
const TOKEN_RE = /(^|\s)(\/[\w-]*)$/

const matchToken = (value, caret) => {
    const m = value.slice(0, caret).match(TOKEN_RE)
    return m ? m[2] : null
}

const CommandInput = ({ value, onChange, multiline = false, minRows, placeholder, fullWidth, size = 'small', autoFocus, sx }) => {
    const inputRef = useRef(null)
    const anchorRef = useRef(null)
    const [open, setOpen] = useState(false)
    const [matches, setMatches] = useState([])
    const [hi, setHi] = useState(0)

    const recompute = (val, caret) => {
        const token = matchToken(val, caret)
        if (token === null) { setOpen(false); return }
        const q = token.toLowerCase()
        const list = DETRITUS_COMMANDS.filter((c) => c.cmd.startsWith(q) || c.cmd.includes(q.slice(1)))
        setMatches(list)
        setHi(0)
        setOpen(list.length > 0)
    }

    const handleChange = (e) => {
        onChange(e.target.value)
        recompute(e.target.value, e.target.selectionStart)
    }

    const insert = (cmd) => {
        const el = inputRef.current
        const caret = el ? el.selectionStart : value.length
        const token = matchToken(value, caret)
        if (token === null) return
        const start = caret - token.length
        const next = value.slice(0, start) + cmd + ' ' + value.slice(caret)
        onChange(next)
        setOpen(false)
        requestAnimationFrame(() => {
            if (!el) return
            el.focus()
            const pos = start + cmd.length + 1
            el.setSelectionRange(pos, pos)
        })
    }

    const onKeyDown = (e) => {
        if (!open || !matches.length) return
        if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => (h + 1) % matches.length) }
        else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => (h - 1 + matches.length) % matches.length) }
        else if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); insert(matches[hi].cmd) }
        else if (e.key === 'Escape') { e.preventDefault(); setOpen(false) }
    }

    return (
        <Box ref={anchorRef} sx={{ position: 'relative', ...sx }}>
            <TextField
                inputRef={inputRef}
                value={value}
                onChange={handleChange}
                onKeyDown={onKeyDown}
                onBlur={() => setTimeout(() => setOpen(false), 150)}
                onFocusCapture={(e) => recompute(value, e.target.selectionStart ?? value.length)}
                multiline={multiline}
                minRows={minRows}
                placeholder={placeholder}
                fullWidth={fullWidth}
                size={size}
                autoFocus={autoFocus}
            />
            <Popper open={open} anchorEl={anchorRef.current} placement="bottom-start" style={{ zIndex: 1500, width: anchorRef.current?.clientWidth }}>
                <Paper sx={{ mt: 0.5, maxHeight: 260, overflowY: 'auto', border: '1px solid', borderColor: 'divider', backgroundImage: 'none' }}>
                    <MenuList dense disablePadding>
                        {matches.map((c, i) => (
                            <MenuItem
                                key={c.cmd}
                                selected={i === hi}
                                onMouseDown={(e) => { e.preventDefault(); insert(c.cmd) }}
                                sx={{ alignItems: 'flex-start', py: 0.75 }}
                            >
                                <Box sx={{ minWidth: 0 }}>
                                    <Typography variant="body2" sx={{ fontFamily: 'monospace', color: 'primary.main', fontWeight: 700 }}>{c.cmd}</Typography>
                                    <Typography variant="caption" color="text.secondary" sx={{ whiteSpace: 'normal' }}>{c.desc}</Typography>
                                </Box>
                            </MenuItem>
                        ))}
                    </MenuList>
                </Paper>
            </Popper>
        </Box>
    )
}

export default CommandInput
