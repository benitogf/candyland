import React from 'react'
import { useNavigate } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Link from '@mui/material/Link'
import Tab from '@mui/material/Tab'
import Tabs from '@mui/material/Tabs'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import PauseCircleIcon from '@mui/icons-material/PauseCircle'
import PlayCircleIcon from '@mui/icons-material/PlayCircle'
import StopCircleIcon from '@mui/icons-material/StopCircle'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'

import { STATUS_COLOR, AUTONOMY_LABEL } from '../meta/run'
import { runLabel } from '../util'
import { useQuest, useRuns, isBranchDelivered } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { pauseQuest, resumeQuest, stopQuest } from '../data/api'
import { useToast } from '../feedback'

// Section header used across the detail views.
const Block = ({ title, children }) => (
    <Card sx={{ mb: 2.5 }}>
        <CardContent>
            <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1 }}>{title}</Typography>
            {children}
        </CardContent>
    </Card>
)

const Empty = ({ children }) => <Typography variant="body2" color="text.secondary">{children}</Typography>

// Quest control: pause/resume/stop are the only quest controls the backend
// exposes (lean, flow-level — no per-tick control). Disabled when offline.
const QuestControls = ({ quest, reachable, onPause, onResume, onStop }) => {
    const offline = reachable === false
    const tip = offline ? 'Server unreachable — start ./candyland to control this quest' : ''
    const terminal = quest.status === 'done' || quest.status === 'stopped'
    if (terminal) return <Chip label={quest.status} size="small" color={STATUS_COLOR[quest.status] || 'default'} variant="outlined" sx={{ flexShrink: 0 }} />
    return (
        <Tooltip title={tip} disableHoverListener={!offline}>
            <Box component="span" sx={{ display: 'flex', gap: 1, flexShrink: 0 }}>
                {quest.status === 'paused'
                    ? <Button color="primary" variant="contained" startIcon={<PlayCircleIcon />} disabled={offline} onClick={onResume}>Resume</Button>
                    : <Button color="inherit" variant="outlined" startIcon={<PauseCircleIcon />} disabled={offline} onClick={onPause}>Pause</Button>}
                <Button color="error" variant="outlined" startIcon={<StopCircleIcon />} disabled={offline} onClick={onStop}>Stop</Button>
            </Box>
        </Tooltip>
    )
}

// One findings row: a work item with its disposition and child run link.
const Finding = ({ item, onRun }) => (
    <Box sx={{ py: 1, borderBottom: '1px solid', borderColor: 'divider' }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
            {item.classification && <Chip size="small" variant="outlined" label={item.classification} sx={{ height: 20 }} />}
            {item.disposition && <Chip size="small" color={item.disposition === 'completed' ? 'success' : item.disposition === 'blocked' ? 'warning' : 'default'} variant="outlined" label={item.disposition} sx={{ height: 20 }} />}
            {item.decision && <Typography variant="caption" color="text.secondary">decision: {item.decision}</Typography>}
            {item.childRunId && <Link component="button" type="button" onClick={() => onRun(item.childRunId)} sx={{ fontFamily: 'monospace', fontSize: 12 }}>{item.childRunId}</Link>}
        </Box>
        {item.evidence && <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>{item.evidence}</Typography>}
    </Box>
)

const QuestWorkspace = ({ id, onClose }) => {
    const navigate = useNavigate()
    const { reachable } = useSystemStatus()
    const toast = useToast()
    const quest = useQuest(id)
    const allRuns = useRuns()

    if (!quest) {
        return (
            <Dialog fullScreen open onClose={onClose} aria-label="Connecting to the quest" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none' } }}>
                <Box sx={{ display: 'flex', justifyContent: 'flex-end', p: 1 }}><IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton></Box>
                <Box sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2 }}>
                    <CircularProgress />
                    <Typography variant="body2" color="text.secondary">Connecting to the quest…</Typography>
                </Box>
            </Dialog>
        )
    }

    const [tab, setTab] = React.useState('activity')
    const fail = (e) => toast(e?.message || 'Command failed — is the candyland server reachable?')
    const childRuns = allRuns.filter((r) => r.questId === quest.id)
    const ticks = quest.ticks || []
    const currentTick = ticks[ticks.length - 1] || null
    const prs = quest.prs || []
    const openRun = (runId) => navigate(`/run/${runId}`)

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Quest workspace" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, pt: 2, pb: 2 }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2 }}>
                    <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                            <Chip size="small" color="secondary" variant="outlined" label={`quest · ${quest.id}`} />
                            <Chip size="small" color={STATUS_COLOR[quest.status] || 'default'} variant="outlined" label={quest.status} />
                            {quest.autonomyLevel && <Chip size="small" variant="outlined" label={AUTONOMY_LABEL[quest.autonomyLevel] || quest.autonomyLevel} />}
                            {quest.campaignId && <Link component="button" type="button" onClick={() => navigate(`/campaign/${quest.campaignId}`)} sx={{ fontFamily: 'monospace', fontSize: 12 }}>↑ {quest.campaignId}</Link>}
                        </Box>
                        <Typography variant="h5" sx={{ fontWeight: 800, mt: 0.5 }}>{quest.objective || quest.originalObjective || quest.id}</Typography>
                    </Box>
                    <QuestControls
                        quest={quest} reachable={reachable}
                        onPause={() => pauseQuest(id).catch(fail)}
                        onResume={() => resumeQuest(id).catch(fail)}
                        onStop={() => stopQuest(id).catch(fail)}
                    />
                    <IconButton onClick={onClose} aria-label="close" sx={{ flexShrink: 0 }}><CloseIcon /></IconButton>
                </Box>
            </Box>

            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 } }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto' }}>
                    <Tabs value={tab} onChange={(_, v) => setTab(v)} aria-label="Quest detail sections">
                        <Tab value="activity" label="Activity" />
                        <Tab value="objective" label="Objective & intent" />
                    </Tabs>
                </Box>
            </Box>

            <Box sx={{ flexGrow: 1, overflowY: 'auto', overflowX: 'hidden' }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto', px: { xs: 2, sm: 4 }, py: 3 }}>
                    {quest.pauseReason && (quest.status === 'paused' || quest.status === 'blocked') && (
                        <Alert severity="warning" variant="outlined" sx={{ mb: 2.5 }}>Blocker: {quest.pauseReason}</Alert>
                    )}

                    {tab === 'objective' && (
                        <Block title="objective">
                            <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap' }}>{quest.originalObjective || quest.objective}</Typography>
                            {quest.scope && <Typography variant="body2" sx={{ mt: 1 }}><b>Scope:</b> {quest.scope}</Typography>}
                        </Block>
                    )}

                    {tab === 'activity' && (
                    <>
                    <Block title="current tick">
                        {currentTick ? (
                            <>
                                <Typography variant="body2" sx={{ fontFamily: 'monospace', mb: 0.5 }}>{currentTick.id} · tick {ticks.length} of {ticks.length}</Typography>
                                {currentTick.discoverySummary && <Typography variant="body2" color="text.secondary">{currentTick.discoverySummary}</Typography>}
                                {currentTick.nextAction && <Typography variant="body2" sx={{ mt: 1 }}><b>Next:</b> {currentTick.nextAction}</Typography>}
                            </>
                        ) : <Empty>No ticks yet.</Empty>}
                    </Block>

                    <Block title={`findings · ${(quest.workItems || []).length}`}>
                        {(quest.workItems || []).length === 0
                            ? <Empty>No work items discovered yet.</Empty>
                            : (quest.workItems || []).map((it) => <Finding key={it.id} item={it} onRun={openRun} />)}
                    </Block>

                    <Block title={`child runs · ${childRuns.length}`}>
                        {childRuns.length === 0
                            ? <Empty>No child runs launched yet.</Empty>
                            : childRuns.map((r) => (
                                <Box key={r.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
                                    <Link component="button" type="button" onClick={() => openRun(r.id)} sx={{ fontWeight: 600 }}>{runLabel(r)}</Link>
                                    <Chip size="small" variant="outlined" color={STATUS_COLOR[r.status] || 'default'} label={r.status} sx={{ height: 20 }} />
                                    {isBranchDelivered(r)
                                        ? <Chip size="small" variant="outlined" color="secondary" label="committed" title="Committed to the campaign branch — the parent opens the PR" sx={{ height: 20, ml: 'auto' }} />
                                        : r.prUrl && <Link href={r.prUrl} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5, ml: 'auto' }}>PR <OpenInNewIcon sx={{ fontSize: 13 }} /></Link>}
                                </Box>
                            ))}
                    </Block>

                    <Block title={`PRs · ${prs.length}`}>
                        {prs.length === 0
                            ? <Empty>No PRs opened yet ({quest.prsOpened || 0} reported).</Empty>
                            : prs.map((p, i) => (
                                <Box key={i} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.5 }}>
                                    <Typography variant="caption" sx={{ fontFamily: 'monospace' }}>{p.repo}</Typography>
                                    {p.url
                                        ? <Link href={p.url} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>PR <OpenInNewIcon sx={{ fontSize: 13 }} /></Link>
                                        : <Typography variant="caption" color="error">{p.err || 'failed'}</Typography>}
                                </Box>
                            ))}
                    </Block>
                    </>
                    )}
                </Box>
            </Box>
        </Dialog>
    )
}

export default QuestWorkspace
