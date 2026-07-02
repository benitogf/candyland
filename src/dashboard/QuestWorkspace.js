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
import StopCircleIcon from '@mui/icons-material/StopCircle'

import { STATUS_COLOR, AUTONOMY_LABEL } from '../meta/run'
import { runLabel } from '../util'
import { useQuest, useRuns } from '../data/ooo'
import { useSystemStatus } from '../data/system'
import { stopQuest } from '../data/api'
import { useToast } from '../feedback'
import ConfirmStopDialog from '../components/ConfirmStopDialog'
import { CopyPrLink } from '../components/CopyPr'
import { Stat, StatGrid, RepoDelivery, AgentActivity, isFinished, shortTime } from './rollup'

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

// Quest control: Stop is the only interaction (lean, flow-level — no pause,
// resume, or per-tick control). Stop is terminal, irreversible, and CASCADES to
// the quest's child runs, so it goes through a confirmation naming that scope.
const QuestControls = ({ quest, reachable, childRunCount, onStop }) => {
    const [confirm, setConfirm] = React.useState(false)
    const offline = reachable === false
    const tip = offline ? 'Server unreachable — start ./candyland to control this quest' : ''
    const terminal = quest.status === 'done' || quest.status === 'stopped'
    if (terminal) return <Chip label={quest.status} size="small" color={STATUS_COLOR[quest.status] || 'default'} variant="outlined" sx={{ flexShrink: 0 }} />
    const scope = childRunCount > 0
        ? `this quest and its ${childRunCount} run${childRunCount === 1 ? '' : 's'}`
        : 'this quest'
    return (
        <Tooltip title={tip} disableHoverListener={!offline}>
            <Box component="span" sx={{ display: 'flex', gap: 1, flexShrink: 0 }}>
                <Button color="error" variant="outlined" startIcon={<StopCircleIcon />} disabled={offline} onClick={() => setConfirm(true)}>Stop</Button>
                <ConfirmStopDialog
                    open={confirm} what="this quest" scope={scope}
                    onCancel={() => setConfirm(false)}
                    onConfirm={() => { setConfirm(false); onStop() }}
                />
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
    // PRs the quest reported: its own list plus any opened during ticks — the
    // per-repo delivery rollup merges them.
    const tickPrs = ticks.flatMap((t) => t.prs || [])
    const prs = [...(quest.prs || []), ...tickPrs]
    const runsDone = childRuns.filter((r) => isFinished(r.status)).length
    const openRun = (runId) => navigate(`/run/${runId}`)

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Quest workspace" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, pt: 2, pb: 2 }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap' }}>
                    <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                            <Chip size="small" color="secondary" variant="outlined" label={`quest · ${quest.id}`} sx={{ maxWidth: '100%' }} />
                            <Chip size="small" color={STATUS_COLOR[quest.status] || 'default'} variant="outlined" label={quest.status} />
                            {quest.autonomyLevel && <Chip size="small" variant="outlined" label={AUTONOMY_LABEL[quest.autonomyLevel] || quest.autonomyLevel} />}
                            {quest.campaignId && <Link component="button" type="button" onClick={() => navigate(`/campaign/${quest.campaignId}`)} sx={{ fontFamily: 'monospace', fontSize: 12 }}>↑ {quest.campaignId}</Link>}
                        </Box>
                        <Typography variant="h5" sx={{ fontWeight: 800, mt: 0.5, overflowWrap: 'anywhere' }}>{quest.objective || quest.originalObjective || quest.id}</Typography>
                    </Box>
                    <QuestControls
                        quest={quest} reachable={reachable} childRunCount={childRuns.length}
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
                    <Block title="rollup">
                        <StatGrid done={runsDone} total={childRuns.length}>
                            <Stat label="items done" value={quest.itemsCompleted || 0} sub={`${quest.itemsSkipped || 0} skipped · ${quest.itemsBlocked || 0} blocked`} color="success.main" />
                            <Stat label="child runs" value={`${runsDone}/${childRuns.length}`} />
                            <Stat label="PRs opened" value={quest.prsOpened || 0} />
                            <Stat label="ticks" value={ticks.length} />
                            <Stat label="tokens" value={(quest.tokensUsed || 0).toLocaleString()} sub={quest.tokenBudget ? `of ${quest.tokenBudget.toLocaleString()}` : undefined} />
                        </StatGrid>
                        <Box sx={{ mt: 2 }}>
                            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>per-repo delivery</Typography>
                            <RepoDelivery prs={prs} />
                        </Box>
                        <Box sx={{ mt: 2 }}>
                            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>agent activity</Typography>
                            <AgentActivity entities={[...childRuns, quest]} />
                        </Box>
                        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
                            created {shortTime(quest.createdAt)} · updated {shortTime(quest.updatedAt)}{quest.lastProgress ? ` · last progress ${shortTime(quest.lastProgress)}` : ''}
                        </Typography>
                    </Block>

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

                    {/* Child runs are listed as drill-downs only — a link plus status.
                        Per-run delivery detail (PR links, branch-committed state) is
                        run-level and lives in the run workspace; the quest's own
                        deliverables are aggregated in the PRs block below. */}
                    <Block title={`child runs · ${childRuns.length}`}>
                        {childRuns.length === 0
                            ? <Empty>No child runs launched yet.</Empty>
                            : childRuns.map((r) => (
                                <Box key={r.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
                                    <Link component="button" type="button" onClick={() => openRun(r.id)} sx={{ fontWeight: 600, minWidth: 0, textAlign: 'left', overflowWrap: 'anywhere' }}>{runLabel(r)}</Link>
                                    <Chip size="small" variant="outlined" color={STATUS_COLOR[r.status] || 'default'} label={r.status} sx={{ height: 20 }} />
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
                                        ? <CopyPrLink url={p.url} />
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
