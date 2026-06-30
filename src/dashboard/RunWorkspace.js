import React, { useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Step from '@mui/material/Step'
import StepLabel from '@mui/material/StepLabel'
import LinearProgress from '@mui/material/LinearProgress'
import Stepper from '@mui/material/Stepper'
import Tab from '@mui/material/Tab'
import Tabs from '@mui/material/Tabs'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import StopCircleIcon from '@mui/icons-material/StopCircle'
import ReplayIcon from '@mui/icons-material/Replay'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import EditIcon from '@mui/icons-material/Edit'

import { PHASES } from '../meta/run'
import { runLabel } from '../util'
import { StateChip, StateLegend } from '../components/StatusBits'
import EditRunDialog from '../components/EditRunDialog'
import RunSwitcher from './RunSwitcher'
import AgentsPanel from '../panels/AgentsPanel'
import BoardPanel from '../panels/BoardPanel'
import TasksPanel from '../panels/TasksPanel'
import SessionsPanel from '../panels/SessionsPanel'
import AuditPanel from '../panels/AuditPanel'

const TABS = [
    { key: 'overview', label: 'Overview' },
    { key: 'agents', label: 'Agents' },
    { key: 'board', label: 'Board' },
    { key: 'tasks', label: 'Tasks' },
    { key: 'sessions', label: 'Sessions' },
    { key: 'audit', label: 'Audit' },
]

// ── Developer: the full, detailed view ──────────────────────────────────────
const Meter = ({ label, right, value, color }) => (
    <Box sx={{ mb: 1.5 }}>
        <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 0.5 }}>
            <Typography variant="caption" color="text.secondary">{label}</Typography>
            <Typography variant="caption" color="text.secondary">{right}</Typography>
        </Box>
        <LinearProgress variant="determinate" value={Math.min(100, value)} color={color} sx={{ height: 7, borderRadius: 4 }} />
    </Box>
)

// General run summary — budget + completion. Applies to any run (no per-task
// assumptions), replaces the earlier per-agent chart.
const OverviewPanel = ({ run }) => (
    <Box>
        {run.prompt && (
            <Card sx={{ mb: 3 }}>
                <CardContent>
                    <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 0.5 }}>your request</Typography>
                    <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{run.prompt}</Typography>
                </CardContent>
            </Card>
        )}

        <Card sx={{ mb: 3 }}>
            <CardContent>
                <Meter label="budget used" right={`${run.tokensUsed}k / ${run.tokensBudget}k · $${run.costUsd.toFixed(2)}`} value={(run.tokensUsed / run.tokensBudget) * 100} color={run.tokensUsed / run.tokensBudget > 0.85 ? 'warning' : 'info'} />
                <Meter label="tasks complete" right={`${run.tasksGreen} / ${run.tasksTotal}`} value={run.tasksTotal ? (run.tasksGreen / run.tasksTotal) * 100 : 0} color="secondary" />
            </CardContent>
        </Card>

        <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1 }}>the fleet</Typography>
        <Box sx={{ display: 'flex', gap: 1.25, flexWrap: 'wrap', mb: 3 }}>
            {run.agents.length === 0
                ? <Typography variant="body2" color="text.secondary">No agents spawned yet — still planning.</Typography>
                : run.agents.map((a) => (
                    <Card key={a.id} title={a.activity} sx={{ pl: 1.75, pr: 1.5, py: 1.25, display: 'flex', alignItems: 'center', gap: 1.25, maxWidth: '100%' }}>
                        <Typography variant="body2" noWrap>{a.emoji} {a.role}</Typography>
                        <StateChip state={a.state} />
                    </Card>
                ))}
        </Box>

        <Card sx={{ borderLeft: '3px solid', borderColor: 'warning.main', backgroundColor: 'rgba(255, 217, 61, 0.06)' }}>
            <CardContent>
                <Chip label="how this works" size="small" color="warning" variant="outlined" sx={{ mb: 1.5, fontWeight: 700 }} />
                <Typography variant="body2" color="text.secondary">
                    Every tab is one lens on the <em>same</em> run, derived from each agent's <code>stream-json</code> stream plus the test runs the
                    conductor owns — state is never self-reported (<b>green</b> = the defining test passing). The deliverable is one PR; merging stays your call.
                </Typography>
            </CardContent>
        </Card>
    </Box>
)

// Scroll wrapper for document-flow panels (Overview / Board / Tasks / Simple) so
// their overflow scrolls inside the body region, never the dialog itself.
const Scrollable = ({ children }) => (
    <Box sx={{ height: { md: '100%' }, overflowY: { md: 'auto' }, overflowX: 'hidden' }}>{children}</Box>
)

// Agents/Sessions fill the height and own their internal scroll; the rest scroll
// inside a wrapper. Either way, overflow stays inside the body — not the layout.
const panelFor = (key, run) => {
    if (key === 'agents') return <AgentsPanel run={run} />
    if (key === 'sessions') return <SessionsPanel run={run} />
    if (key === 'board') return <Scrollable><BoardPanel run={run} /></Scrollable>
    if (key === 'tasks') return <Scrollable><TasksPanel run={run} /></Scrollable>
    if (key === 'audit') return <Scrollable><AuditPanel run={run} /></Scrollable>
    return <Scrollable><OverviewPanel run={run} /></Scrollable>
}

// ── Header controls — Stop / Restart, gated by status. Candyland keeps a lean,
//    flow-level control surface (no per-agent control, no resume). ────────────
const RunControls = ({ run, controls, done, onEdit }) => {
    const offline = controls.reachable === false
    const offlineTip = 'Server unreachable — start ./candyland to control this run'
    const RestartButton = ({ label }) => (
        <Tooltip title={offline ? offlineTip : 'Re-run this task as-is'} disableHoverListener={false}>
            <Box component="span">
                <Button color="primary" variant="contained" startIcon={<ReplayIcon />} disabled={offline} onClick={controls.restart}>{label}</Button>
            </Box>
        </Tooltip>
    )
    // Edit re-opens the task setup (changing it re-plans) — distinct from Restart
    // (re-run as-is). Offered on finished runs.
    const EditButton = () => (
        <Tooltip title={offline ? offlineTip : 'Change the request and re-plan'} disableHoverListener={false}>
            <Box component="span">
                <Button color="inherit" variant="outlined" startIcon={<EditIcon />} disabled={offline} onClick={onEdit}>Edit</Button>
            </Box>
        </Tooltip>
    )

    if (!controls.controllable) {
        return done && run.prUrl
            ? <Button component="a" href={run.prUrl} target="_blank" rel="noreferrer" color="secondary" variant="outlined" endIcon={<OpenInNewIcon />} sx={{ flexShrink: 0 }}>PR #{run.prUrl.split('/').pop()}</Button>
            : <Chip label="snapshot" size="small" variant="outlined" sx={{ flexShrink: 0 }} />
    }
    if (controls.status === 'cancelled') {
        return <Chip label="cancelled" size="small" color="default" variant="outlined" sx={{ flexShrink: 0 }} />
    }
    if (controls.status === 'done') {
        return (
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexShrink: 0 }}>
                {run.error
                    ? <><Chip label="failed" size="small" color="error" variant="outlined" /><RestartButton label="Restart" /></>
                    : run.prUrl
                        ? <Button component="a" href={run.prUrl} target="_blank" rel="noreferrer" color="secondary" variant="outlined" endIcon={<OpenInNewIcon />}>PR #{run.prUrl.split('/').pop()}</Button>
                        : <Chip label="completed" size="small" color="success" variant="outlined" />}
                <EditButton />
            </Box>
        )
    }
    if (controls.status === 'paused') {
        return (
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexShrink: 0 }}>
                <Chip label="stopped" size="small" color="warning" variant="outlined" />
                <RestartButton label="Restart" />
                <EditButton />
            </Box>
        )
    }
    return (
        <Tooltip title={offline ? offlineTip : ''} disableHoverListener={!offline}>
            <Box component="span" sx={{ flexShrink: 0 }}>
                <Button color="error" variant="outlined" startIcon={<StopCircleIcon />} disabled={offline} onClick={controls.stop}>Stop run</Button>
            </Box>
        </Tooltip>
    )
}

// Cancel is the flow-level control during planning: the run hasn't started, so
// there's nothing to "stop" — Cancel abandons it (deletes it) and returns to the
// dashboard. Always enabled so the user is never trapped.
const CancelControl = ({ onCancel }) => (
    <Button color="error" variant="outlined" startIcon={<StopCircleIcon />} sx={{ flexShrink: 0 }} onClick={onCancel}>Cancel run</Button>
)

const RunWorkspace = ({ run, controls, planning, tab, onClose, onTab }) => {
    const [editing, setEditing] = useState(false)
    const isPlanning = !!planning
    const active = TABS.some((t) => t.key === tab) ? tab : 'overview'
    const done = controls.controllable ? controls.status === 'done' : run.phase >= PHASES.length - 1
    const repo = run.folders?.[0] || run.branch // the run's primary working folder
    const showTabs = !isPlanning
    // Real, functional completion — moves over the live run (elapsed-driven), or
    // reflects the phase for a static snapshot.
    const progressPct = Math.round(100 * (run.progress ?? (run.phase / (PHASES.length - 1))))

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Run workspace" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            {/* Header — aligned to the body column */}
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, pt: 2 }}>
                <Box sx={{ maxWidth: 1180, mx: 'auto' }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                        <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                            <Typography variant="h5" sx={{ fontWeight: 800 }} noWrap>{runLabel(run)}</Typography>
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mt: 0.25 }}>
                                <Typography variant="body2" color="text.secondary" noWrap sx={{ fontFamily: 'monospace', maxWidth: '60vw' }}>{repo} · {run.branch}</Typography>
                            </Box>
                        </Box>
                        {isPlanning
                            ? <CancelControl onCancel={controls.cancel} />
                            : <RunControls run={run} controls={controls} done={done} onEdit={() => setEditing(true)} />}
                        <RunSwitcher current={{ id: run.id, label: runLabel(run) }} />
                        <IconButton onClick={onClose} aria-label="close" sx={{ flexShrink: 0 }}><CloseIcon /></IconButton>
                    </Box>

                    <Box sx={{ maxWidth: 720, mx: 'auto', mt: 2 }}>
                        <Stepper activeStep={run.phase} alternativeLabel>
                            {PHASES.map((p) => <Step key={p}><StepLabel>{p}</StepLabel></Step>)}
                        </Stepper>
                        {/* Functional progress — moves with the run, not a legend */}
                        <Box sx={{ mt: 1 }}>
                            <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 0.5 }}>
                                <Typography variant="caption" color="text.secondary">{isPlanning ? 'Planning…' : `${PHASES[run.phase]} · ${progressPct}%`}</Typography>
                                <Typography variant="caption" color="text.secondary">{run.tasksGreen}/{run.tasksTotal} tasks</Typography>
                            </Box>
                            <LinearProgress
                                variant={isPlanning ? 'indeterminate' : 'determinate'}
                                value={progressPct}
                                aria-label="run progress"
                                sx={{ height: 6, borderRadius: 4 }}
                            />
                        </Box>
                    </Box>

                    {showTabs && (
                        <Tabs value={active} onChange={(_, v) => onTab(v)} sx={{ mt: 1.5 }} variant="scrollable" scrollButtons="auto">
                            {TABS.map((t) => <Tab key={t.key} value={t.key} label={t.label} />)}
                        </Tabs>
                    )}
                </Box>
            </Box>

            {showTabs && (
                <Box sx={{ px: { xs: 2, sm: 4 }, py: 1, borderBottom: '1px solid', borderColor: 'divider', backgroundColor: 'background.paper' }}>
                    <Box sx={{ maxWidth: 1180, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap' }}>
                        <Typography variant="overline" color="text.disabled" sx={{ flexShrink: 0 }}>legend</Typography>
                        <StateLegend />
                    </Box>
                </Box>
            )}

            {/* Run-level error advisory — visible across tabs */}
            {run.error && (
                <Box sx={{ px: { xs: 2, sm: 4 }, pt: 1.5 }}>
                    <Box sx={{ maxWidth: 1180, mx: 'auto' }}>
                        <Alert severity="error" variant="outlined">{run.error}</Alert>
                    </Box>
                </Box>
            )}

            {/* Body — desktop: fixed region, panels scroll internally. Mobile: the
                body scrolls vertically (panels are natural height). No horizontal scroll either way. */}
            <Box sx={{ flexGrow: 1, minHeight: 0, overflowX: 'hidden', overflowY: { xs: 'auto', md: 'hidden' }, display: { xs: 'block', md: 'flex' } }}>
                <Box sx={{ width: '100%', maxWidth: 1180, mx: 'auto', px: { xs: 2, sm: 4 }, py: 3, minWidth: 0, display: { xs: 'block', md: 'flex' }, flexDirection: 'column', minHeight: 0 }}>
                    {isPlanning
                        ? <Scrollable>{planning}</Scrollable>
                        : panelFor(active, run)}
                </Box>
            </Box>
            <EditRunDialog run={run} open={editing} onClose={() => setEditing(false)} />
        </Dialog>
    )
}

export default RunWorkspace
