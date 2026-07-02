import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Link from '@mui/material/Link'
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
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import CallMergeIcon from '@mui/icons-material/CallMerge'

import { PHASES } from '../meta/run'
import { runLabel } from '../util'
import { deliverOf } from '../data/ooo'
import { StateChip, LegendButton } from '../components/StatusBits'
import ConfirmStopDialog from '../components/ConfirmStopDialog'
import AgentsPanel from '../panels/AgentsPanel'
import TasksPanel from '../panels/TasksPanel'

// Agents (live states + full output) is the default lens for a task run — the
// thing you want on landing. Overview/intent is a secondary tab.
const TABS = [
    { key: 'agents', label: 'Agents' },
    { key: 'overview', label: 'Overview' },
    { key: 'tasks', label: 'Tasks' },
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
const OverviewPanel = ({ run }) => {
    // A task-run is a child launched by a quest/campaign — the parent owns the
    // program-level delivery narrative, so we drop the "one PR" system framing
    // here and keep the view to this run's own info only.
    const isTaskRun = !!(run.questId || run.campaignId)
    return (
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

        {!isTaskRun && (
            <Card sx={{ borderLeft: '3px solid', borderColor: 'warning.main', backgroundColor: 'rgba(255, 217, 61, 0.06)' }}>
                <CardContent>
                    <Chip label="how this works" size="small" color="warning" variant="outlined" sx={{ mb: 1.5, fontWeight: 700 }} />
                    <Typography variant="body2" color="text.secondary">
                        Every tab is one lens on the <em>same</em> run, derived from each agent's <code>stream-json</code> stream plus the test runs the
                        conductor owns — state is never self-reported (<b>green</b> = the defining test passing). The deliverable is one PR; merging stays your call.
                    </Typography>
                </CardContent>
            </Card>
        )}
    </Box>
    )
}

// Scroll wrapper for document-flow panels (Overview / Tasks / Simple) so
// their overflow scrolls inside the body region, never the dialog itself.
const Scrollable = ({ children }) => (
    <Box sx={{ height: { md: '100%' }, overflowY: { md: 'auto' }, overflowX: 'hidden' }}>{children}</Box>
)

// Agents fill the height and own their internal scroll; the rest scroll
// inside a wrapper. Either way, overflow stays inside the body — not the layout.
const panelFor = (key, run) => {
    if (key === 'agents') return <AgentsPanel run={run} />
    if (key === 'tasks') return <Scrollable><TasksPanel run={run} /></Scrollable>
    return <Scrollable><OverviewPanel run={run} /></Scrollable>
}

// ── Header controls — Stop / Restart, gated by status. Candyland keeps a lean,
//    flow-level control surface (no per-agent control, no resume). ────────────
// A branch-delivered child commits to the shared campaign branch and opens no PR
// of its own — the parent opens the PR. Show this as a POSITIVE outcome, never a
// missing-PR. Branch label falls back to the run's branch.
const BranchDelivered = ({ run }) => (
    <Tooltip title="Committed to the campaign branch — the parent campaign opens the PR">
        <Chip
            icon={<CallMergeIcon />}
            label={`committed to ${run.branch || 'branch'}`}
            size="small" color="secondary" variant="outlined" sx={{ flexShrink: 0, maxWidth: 280 }}
        />
    </Tooltip>
)

// Feedback delivery: the run addressed review feedback and UPDATED an existing
// PR in place — it opens NO new PR. run.prUrl is the existing/updated PR, so we
// link it as a positive "updated in place" outcome, never a new-PR affordance
// and never a missing/failed PR.
const FeedbackDelivered = ({ run }) => {
    const num = run.prUrl ? run.prUrl.split('/').pop() : null
    return run.prUrl
        ? (
            <Tooltip title="Addressed review feedback and updated the existing PR in place">
                <Button component="a" href={run.prUrl} target="_blank" rel="noreferrer" color="secondary" variant="outlined" endIcon={<OpenInNewIcon />} sx={{ flexShrink: 0 }}>
                    Updated PR #{num}
                </Button>
            </Tooltip>
        )
        : (
            <Chip label="feedback applied" size="small" color="success" variant="outlined" sx={{ flexShrink: 0 }} />
        )
}

// Review delivery: the run reviewed a PR. Either findings were applied to that
// PR (link it), or there were no actionable findings — a clean, intentional
// no-PR outcome, NOT a missing/failed PR.
const ReviewDelivered = ({ run }) => {
    const num = run.prUrl ? run.prUrl.split('/').pop() : null
    return run.prUrl
        ? (
            <Tooltip title="Reviewed — findings applied to the PR">
                <Button component="a" href={run.prUrl} target="_blank" rel="noreferrer" color="secondary" variant="outlined" endIcon={<OpenInNewIcon />} sx={{ flexShrink: 0 }}>
                    Reviewed · PR #{num}
                </Button>
            </Tooltip>
        )
        : (
            <Tooltip title="Reviewed — no actionable findings; nothing to apply">
                <Chip label="reviewed · no findings" size="small" color="success" variant="outlined" sx={{ flexShrink: 0, maxWidth: 280 }} />
            </Tooltip>
        )
}

// The positive terminal rendering for a finished run, keyed on its delivery
// shape. Returns null when the shape is a plain PR run, so callers keep their
// own PR / completed handling for the default case.
const DeliveryOutcome = ({ run }) => {
    const shape = deliverOf(run)
    if (shape === 'branch') return <BranchDelivered run={run} />
    if (shape === 'feedback') return <FeedbackDelivered run={run} />
    if (shape === 'review') return <ReviewDelivered run={run} />
    return null
}

// Stop is the ONLY live-run control (no restart, edit, pause, or resume). A
// finished / failed / stopped run shows its terminal outcome and nothing more —
// runs are not re-runnable from the UI.
const RunControls = ({ run, controls, done }) => {
    const [confirm, setConfirm] = useState(false)
    const offline = controls.reachable === false
    const outcome = <DeliveryOutcome run={run} />
    const hasOutcome = deliverOf(run) !== 'pr'

    if (!controls.controllable) {
        if (done && hasOutcome) return outcome
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
                    ? <Chip label="failed" size="small" color="error" variant="outlined" />
                    : hasOutcome
                        ? outcome
                        : run.prUrl
                            ? <Button component="a" href={run.prUrl} target="_blank" rel="noreferrer" color="secondary" variant="outlined" endIcon={<OpenInNewIcon />}>PR #{run.prUrl.split('/').pop()}</Button>
                            : <Chip label="completed" size="small" color="success" variant="outlined" />}
            </Box>
        )
    }
    if (controls.status === 'paused') {
        return <Chip label="stopped" size="small" color="warning" variant="outlined" sx={{ flexShrink: 0 }} />
    }
    return (
        <Tooltip title={offline ? 'Server unreachable — start ./candyland to control this run' : ''} disableHoverListener={!offline}>
            <Box component="span" sx={{ flexShrink: 0 }}>
                <Button color="error" variant="outlined" startIcon={<StopCircleIcon />} disabled={offline} onClick={() => setConfirm(true)}>Stop run</Button>
                <ConfirmStopDialog
                    open={confirm} what="this run" scope="this run"
                    onCancel={() => setConfirm(false)}
                    onConfirm={() => { setConfirm(false); controls.stop() }}
                />
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
    const navigate = useNavigate()
    const isPlanning = !!planning
    // The task-run's place in the IA: a child launched by a quest or campaign.
    // Its parent context is a link UP (never embedded), so this view stays scoped
    // to run-level info only — matching how the quest view links up to a campaign.
    const parent = run.questId
        ? { kind: 'quest', id: run.questId, path: `/quest/${run.questId}` }
        : run.campaignId
            ? { kind: 'campaign', id: run.campaignId, path: `/campaign/${run.campaignId}` }
            : null
    const active = TABS.some((t) => t.key === tab) ? tab : TABS[0].key
    const done = controls.controllable ? controls.status === 'done' : run.phase >= PHASES.length - 1
    const repo = run.folders?.[0] || run.branch // the run's primary working folder
    const showTabs = !isPlanning
    // The final phase isn't always a "PR" step — relabel it per delivery shape so
    // it never reads as a missing PR: branch runs commit to a shared branch,
    // feedback runs update an existing PR in place, review runs apply findings.
    const FINAL_PHASE_LABEL = { branch: 'Commit', feedback: 'Update PR', review: 'Review' }
    const finalLabel = FINAL_PHASE_LABEL[deliverOf(run)]
    const phaseLabels = finalLabel
        ? PHASES.map((p, i) => (i === PHASES.length - 1 ? finalLabel : p))
        : PHASES
    // Real, functional completion — moves over the live run (elapsed-driven), or
    // reflects the phase for a static snapshot.
    const progressPct = Math.round(100 * (run.progress ?? (run.phase / (PHASES.length - 1))))

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Run workspace" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            {/* Header — aligned to the body column */}
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, pt: 2 }}>
                <Box sx={{ maxWidth: 1180, mx: 'auto' }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap' }}>
                        <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.5 }}>
                                <Chip size="small" color="secondary" variant="outlined" label={`run · ${run.id}`} />
                                {parent && (
                                    <Link component="button" type="button" onClick={() => navigate(parent.path)} sx={{ fontFamily: 'monospace', fontSize: 12 }}>↑ {parent.id}</Link>
                                )}
                            </Box>
                            <Typography variant="h5" sx={{ fontWeight: 800 }} noWrap>{runLabel(run)}</Typography>
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mt: 0.25 }}>
                                <Typography variant="body2" color="text.secondary" noWrap sx={{ fontFamily: 'monospace', maxWidth: '100%', overflow: 'hidden', textOverflow: 'ellipsis' }}>{repo} · {run.branch}</Typography>
                            </Box>
                        </Box>
                        {isPlanning
                            ? <CancelControl onCancel={controls.cancel} />
                            : <RunControls run={run} controls={controls} done={done} />}
                        <IconButton onClick={onClose} aria-label="close" sx={{ flexShrink: 0 }}><CloseIcon /></IconButton>
                    </Box>

                    <Box sx={{ maxWidth: 720, mx: 'auto', mt: 2 }}>
                        <Stepper activeStep={run.phase} alternativeLabel>
                            {phaseLabels.map((p) => <Step key={p}><StepLabel>{p}</StepLabel></Step>)}
                        </Stepper>
                        {/* Functional progress — moves with the run, not a legend */}
                        <Box sx={{ mt: 1 }}>
                            <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 0.5 }}>
                                <Typography variant="caption" color="text.secondary">{isPlanning ? 'Planning…' : `${phaseLabels[run.phase]} · ${progressPct}%`}</Typography>
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
                        <Box sx={{ mt: 1.5, display: 'flex', alignItems: 'center', gap: 1 }}>
                            <Tabs value={active} onChange={(_, v) => onTab(v)} sx={{ flexGrow: 1, minWidth: 0 }} variant="scrollable" scrollButtons="auto">
                                {TABS.map((t) => <Tab key={t.key} value={t.key} label={t.label} />)}
                            </Tabs>
                            <LegendButton />
                        </Box>
                    )}
                </Box>
            </Box>

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
        </Dialog>
    )
}

export default RunWorkspace
