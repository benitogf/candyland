import React, { useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import LinearProgress from '@mui/material/LinearProgress'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import HelpOutlineIcon from '@mui/icons-material/HelpOutline'
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked'
import AutorenewIcon from '@mui/icons-material/Autorenew'
import BlockIcon from '@mui/icons-material/Block'
import CallMergeIcon from '@mui/icons-material/CallMerge'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import TaskAltIcon from '@mui/icons-material/TaskAlt'
import StopCircleIcon from '@mui/icons-material/StopCircle'
import ScheduleIcon from '@mui/icons-material/Schedule'
import CloudOffIcon from '@mui/icons-material/CloudOff'
import PauseCircleOutlineIcon from '@mui/icons-material/PauseCircleOutline'

import { candy } from '../config'
import { STATE_META, pauseInfo, resumeAtLabel } from '../meta/run'

// ── Auto-pause indicators ────────────────────────────────────────────────────
// A distinct icon per cause so the two auto-pause reasons are separable without
// reading text: a clock for a time-bounded usage-limit wait, a struck-out cloud
// for a connection/infra outage that's retrying.
const PAUSE_ICON = { limit: ScheduleIcon, connection: CloudOffIcon, other: PauseCircleOutlineIcon }

// The trailing "· resumes at 3:40 PM · paused 2×" detail shared by both surfaces.
const pauseTail = (info) => {
    const at = resumeAtLabel(info.resumeAt)
    const bits = []
    if (at) bits.push(`resumes at ${at}`)
    if (info.rePauses > 0) bits.push(`paused ${info.rePauses}×`)
    return bits.length ? ` · ${bits.join(' · ')}` : ''
}

// Compact paused indicator for list rows / dashboard cards. Renders null unless
// the entity is auto-paused, so callers can drop it in unconditionally. The
// cause label (Usage limit / Connection lost) plus its icon carry the
// distinction; the full backend reason is the tooltip.
export const PauseChip = ({ entity, sx }) => {
    const info = pauseInfo(entity)
    if (!info) return null
    const Icon = PAUSE_ICON[info.cause]
    const at = resumeAtLabel(info.resumeAt)
    const label = at ? `${info.meta.label} · ${at}` : info.meta.label
    return (
        <Chip
            size="small" variant="outlined" color={info.meta.color}
            icon={<Icon />} label={label} title={info.reason}
            sx={{ height: 22, ...sx }}
        />
    )
}

// Full paused banner for the workspace views. Leads with the bold cause label +
// its icon (the visual distinction), then the backend's own pauseReason sentence,
// then the resume time / re-pause count. Null unless auto-paused.
export const PauseBanner = ({ entity, sx }) => {
    const info = pauseInfo(entity)
    if (!info) return null
    const Icon = PAUSE_ICON[info.cause]
    return (
        <Alert severity={info.meta.color} variant="outlined" icon={<Icon />} sx={sx}>
            <b>{info.meta.label}</b> — {info.reason}{pauseTail(info)}
        </Alert>
    )
}

// A distinct shape per state, so the rainbow is readable without relying on
// color alone: queued/working/blocked/integrating read as "in progress",
// green/done read as "complete" (a checkmark).
export const STATE_ICON = {
    idle: RadioButtonUncheckedIcon,
    working: AutorenewIcon,
    retrying: AutorenewIcon,
    blocked: BlockIcon,
    integrating: CallMergeIcon,
    green: CheckCircleIcon,
    done: TaskAltIcon,
    stopped: StopCircleIcon,
}

export const StateIcon = ({ state, size = 15 }) => {
    const m = STATE_META[state] || STATE_META.idle
    // Unknown/empty states (e.g. an agent the backend hasn't stamped yet arrives
    // with state '') must still render — fall back to the idle icon exactly as the
    // STATE_META lookup above does, or <Icon> is undefined and the whole view crashes.
    const Icon = STATE_ICON[state] || STATE_ICON.idle
    const spin = state === 'working' || state === 'retrying'
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
const PROGRESS = ['idle', 'working', 'retrying', 'blocked', 'integrating']
const DONE = ['green', 'done', 'stopped']

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

// An on-demand legend: a small trigger that opens the state legend in a modal,
// keeping it out of the way until the reader asks for it.
export const LegendButton = () => {
    const [open, setOpen] = useState(false)
    return (
        <>
            <Button
                size="small"
                variant="text"
                startIcon={<HelpOutlineIcon />}
                onClick={() => setOpen(true)}
                sx={{ flexShrink: 0, color: 'text.secondary', textTransform: 'none' }}
            >
                legend
            </Button>
            <Dialog open={open} onClose={() => setOpen(false)} maxWidth="sm" fullWidth>
                <DialogTitle sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                    State legend
                    <IconButton onClick={() => setOpen(false)} aria-label="close" size="small"><CloseIcon /></IconButton>
                </DialogTitle>
                <DialogContent>
                    <StateLegend />
                </DialogContent>
            </Dialog>
        </>
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
