import React from 'react'
import Box from '@mui/material/Box'
import ToggleButton from '@mui/material/ToggleButton'
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup'
import Typography from '@mui/material/Typography'

import { useMode } from '../mode'
import { MODES } from '../mock/run'

// The developer / non-developer switch. Reads and writes the app-wide mode, so
// flipping it recolors the whole app (theme primary) wherever it's used.
const ModeToggle = ({ sx }) => {
    const { mode, setMode } = useMode()
    return (
        <ToggleButtonGroup
            exclusive
            value={mode}
            onChange={(_, v) => { if (v) setMode(v) }}
            sx={{
                flexWrap: 'wrap',
                '& .MuiToggleButton-root': {
                    textTransform: 'none', alignItems: 'flex-start', textAlign: 'left',
                    px: 2.5, py: 1.5, flex: '1 1 240px',
                },
                ...sx,
            }}
        >
            {Object.entries(MODES).map(([key, m]) => (
                <ToggleButton key={key} value={key} sx={{ '&.Mui-selected': { borderColor: m.accent, backgroundColor: 'rgba(255,255,255,0.05)' } }}>
                    <Box>
                        <Typography variant="subtitle2" sx={{ fontWeight: 800, color: mode === key ? m.accent : 'text.primary' }}>{m.label}</Typography>
                        <Typography variant="caption" color="text.secondary">{m.tagline}</Typography>
                    </Box>
                </ToggleButton>
            ))}
        </ToggleButtonGroup>
    )
}

export default ModeToggle
