import React from 'react'
import Box from '@mui/material/Box'
import Chip from '@mui/material/Chip'
import LinearProgress from '@mui/material/LinearProgress'
import Typography from '@mui/material/Typography'
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked'
import AutorenewIcon from '@mui/icons-material/Autorenew'
import BlockIcon from '@mui/icons-material/Block'
import CallMergeIcon from '@mui/icons-material/CallMerge'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import TaskAltIcon from '@mui/icons-material/TaskAlt'

import { candy } from '../config'
import { STATE_META, MODES } from '../mock/run'

// A distinct shape per state, so the rainbow is readable without relying on
// color alone: queued/working/blocked/integrating read as "in progress",
// green/done read as "complete" (a checkmark).
export const STATE_ICON = {
    idle: RadioButtonUncheckedIcon,
    working: AutorenewIcon,
    blocked: BlockIcon,
    integrating: CallMergeIcon,
    green: CheckCircleIcon,
    done: TaskAltIcon,
}

export const StateIcon = ({ state, size = 15 }) => {
    const m = STATE_META[state] || STATE_META.idle
    const Icon = STATE_ICON[state]
    const spin = state === 'working'
    return (
        <Icon
            sx={{
                fontSize: size,
                color: m.dot,
                ...(spin && { animation: 'sb-spin 2.4s linear infinite', '@keyframes sb-spin': { to: { transform: 'rotate(360deg)' } } }),
            }}
        />
    )
}

// A state pill: shape + color + label.
export const StateChip = ({ state }) => {
    const m = STATE_META[state] || STATE_META.idle
    return (
        <Chip
            size="small"
            label={m.label}
            variant="outlined"
            sx={{ fontWeight: 700, color: m.color, borderColor: m.dot, '& .MuiChip-icon': { ml: '6px' } }}
            icon={<StateIcon state={state} />}
        />
    )
}

// A two-bucket legend — the distinction the task list needs: in progress vs done.
const PROGRESS = ['idle', 'working', 'blocked', 'integrating']
const DONE = ['green', 'done']

const LegendGroup = ({ title, keys }) => (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25, flexWrap: 'wrap' }}>
        <Typography variant="caption" sx={{ color: 'text.secondary', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.06em', fontSize: 10 }}>
            {title}
        </Typography>
        {keys.map((k) => (
            <Box key={k} sx={{ display: 'flex', alignItems: 'center', gap: 0.4 }}>
                <StateIcon state={k} size={14} />
                <Typography variant="caption" sx={{ color: STATE_META[k].color }}>{STATE_META[k].label}</Typography>
            </Box>
        ))}
    </Box>
)

export const StateLegend = () => (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 3, flexWrap: 'wrap', py: 0.5 }}>
        <LegendGroup title="In progress" keys={PROGRESS} />
        <Box sx={{ width: '1px', height: 16, backgroundColor: 'divider', display: { xs: 'none', sm: 'block' } }} />
        <LegendGroup title="Done" keys={DONE} />
    </Box>
)

// A clear developer / non-developer badge — same look wherever a run is shown.
export const ModeBadge = ({ mode, withTagline = false }) => {
    const m = MODES[mode]
    return (
        <Box sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.75 }}>
            <Chip size="small" label={m.label} sx={{ height: 20, fontSize: 11, fontWeight: 700, color: m.accent, borderColor: m.accent }} variant="outlined" />
            {withTagline && <Typography variant="caption" color="text.secondary">{m.tagline}</Typography>}
        </Box>
    )
}

// A context/token usage bar against the agent's budget.
export const TokenMeter = ({ used, budget }) => {
    const pct = Math.min(100, Math.round((used / budget) * 100))
    return (
        <Box>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 0.5 }}>
                <Typography variant="caption" color="text.secondary">context / tokens</Typography>
                <Typography variant="caption" color="text.secondary">{used}k / {budget}k</Typography>
            </Box>
            <LinearProgress
                variant="determinate"
                value={pct}
                color={pct > 85 ? 'warning' : 'info'}
                sx={{ height: 6, borderRadius: 3, backgroundColor: candy.bgDark }}
            />
        </Box>
    )
}
