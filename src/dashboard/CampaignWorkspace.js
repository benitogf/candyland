import React from 'react'
import { useNavigate } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Link from '@mui/material/Link'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'

import { STATUS_COLOR, AUTONOMY_LABEL } from '../meta/run'
import { runLabel } from '../util'
import { useCampaign, useQuests, useRuns } from '../data/ooo'

const Block = ({ title, children }) => (
    <Card sx={{ mb: 2.5 }}>
        <CardContent>
            <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1 }}>{title}</Typography>
            {children}
        </CardContent>
    </Card>
)

const Empty = ({ children }) => <Typography variant="body2" color="text.secondary">{children}</Typography>

const Bullets = ({ items }) => (
    <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
        {items.map((s, i) => <Typography key={i} component="li" variant="body2" color="text.secondary">{s}</Typography>)}
    </Box>
)

// A gate's pass/pending state. Passed==false with no DecidedAt means "not yet
// decided" (not failed) — see types.go GateResult.
const Gate = ({ label, gate }) => {
    const decided = !!gate?.decidedAt
    const color = !decided ? 'default' : gate.passed ? 'success' : 'error'
    const text = !decided ? 'pending' : gate.passed ? 'passed' : 'failed'
    return (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.5 }}>
            <Chip size="small" variant="outlined" color={color} label={`${label}: ${text}`} sx={{ height: 22 }} />
            {gate?.reason && <Typography variant="caption" color="text.secondary">{gate.reason}</Typography>}
        </Box>
    )
}

const VERDICT_COLOR = { satisfied: 'success', partial: 'warning', missed: 'error' }

const CampaignWorkspace = ({ id, onClose }) => {
    const navigate = useNavigate()
    const campaign = useCampaign(id)
    const allQuests = useQuests()
    const allRuns = useRuns()

    if (!campaign) {
        return (
            <Dialog fullScreen open onClose={onClose} aria-label="Connecting to the campaign" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none' } }}>
                <Box sx={{ display: 'flex', justifyContent: 'flex-end', p: 1 }}><IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton></Box>
                <Box sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2 }}>
                    <CircularProgress />
                    <Typography variant="body2" color="text.secondary">Connecting to the campaign…</Typography>
                </Box>
            </Dialog>
        )
    }

    const brief = campaign.intentBrief || {}
    const commitments = brief.commitments || []
    const verdicts = campaign.intentReview?.verdicts || []
    const verdictFor = (cid) => verdicts.find((v) => v.commitmentId === cid)
    const childQuests = allQuests.filter((q) => q.campaignId === campaign.id || (campaign.questIds || []).includes(q.id))
    // Only the campaign's DIRECT child runs belong at this level. A quest's child
    // runs inherit CampaignID (quest_exec.go), so filtering on campaignId alone
    // would pull those grandchild runs up here — they belong under their quest.
    // Runs launched by the campaign itself carry no questId (linkCampaignChild).
    const childRuns = allRuns.filter((r) => !r.questId && (r.campaignId === campaign.id || (campaign.runIds || []).includes(r.id)))
    const prs = campaign.prs || []
    const routing = campaign.reviewRouting?.length ? campaign.reviewRouting : brief.reviewRouting || []

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Campaign workspace" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, pt: 2, pb: 2 }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2 }}>
                    <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                            <Chip size="small" color="secondary" variant="outlined" label={`campaign · ${campaign.id}`} />
                            <Chip size="small" color={STATUS_COLOR[campaign.status] || 'default'} variant="outlined" label={campaign.status} />
                            {campaign.autonomyLevel && <Chip size="small" variant="outlined" label={AUTONOMY_LABEL[campaign.autonomyLevel] || campaign.autonomyLevel} />}
                        </Box>
                        <Typography variant="h5" sx={{ fontWeight: 800, mt: 0.5 }}>{brief.restatedGoal || campaign.originalInput || campaign.id}</Typography>
                    </Box>
                    {/* Campaign control endpoints don't exist yet — state is read-only. */}
                    <Chip label="read-only" size="small" variant="outlined" sx={{ flexShrink: 0 }} />
                    <IconButton onClick={onClose} aria-label="close" sx={{ flexShrink: 0 }}><CloseIcon /></IconButton>
                </Box>
            </Box>

            <Box sx={{ flexGrow: 1, overflowY: 'auto', overflowX: 'hidden' }}>
                <Box sx={{ maxWidth: 1100, mx: 'auto', px: { xs: 2, sm: 4 }, py: 3 }}>
                    {campaign.pauseReason && (campaign.status === 'paused' || campaign.status === 'blocked') && (
                        <Alert severity="warning" variant="outlined" sx={{ mb: 2.5 }}>Blocker: {campaign.pauseReason}</Alert>
                    )}

                    <Block title="original intent">
                        <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap' }}>{campaign.originalInput}</Typography>
                    </Block>

                    <Block title="intent brief">
                        {brief.restatedGoal
                            ? (
                                <>
                                    <Typography variant="body2"><b>Restated goal:</b> {brief.restatedGoal}</Typography>
                                    {brief.roughSizing && <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>Sizing: {brief.roughSizing}</Typography>}
                                    {brief.scopeByDomain?.length > 0 && <Box sx={{ mt: 1 }}><Typography variant="caption" color="text.secondary">scope by domain</Typography><Bullets items={brief.scopeByDomain} /></Box>}
                                    {commitments.length > 0 && (
                                        <Box sx={{ mt: 1.5 }}>
                                            <Typography variant="caption" color="text.secondary">commitments</Typography>
                                            {commitments.map((c) => (
                                                <Typography key={c.id} component="div" variant="body2" color="text.secondary" sx={{ pl: 1 }}>• {c.statement}</Typography>
                                            ))}
                                        </Box>
                                    )}
                                    {brief.openQuestions?.length > 0 && <Box sx={{ mt: 1 }}><Typography variant="caption" color="text.secondary">open questions</Typography><Bullets items={brief.openQuestions} /></Box>}
                                </>
                            )
                            : <Empty>Brief not yet produced.</Empty>}
                    </Block>

                    <Block title="gates">
                        <Gate label="brief gate" gate={campaign.briefGate} />
                        <Gate label="plan gate" gate={campaign.planGate} />
                    </Block>

                    <Block title={`child quests · ${childQuests.length}`}>
                        {childQuests.length === 0
                            ? <Empty>No child quests launched yet.</Empty>
                            : childQuests.map((q) => (
                                <Box key={q.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
                                    <Link component="button" type="button" onClick={() => navigate(`/quest/${q.id}`)} sx={{ fontWeight: 600 }}>{q.objective || q.id}</Link>
                                    <Chip size="small" variant="outlined" color={STATUS_COLOR[q.status] || 'default'} label={q.status} sx={{ height: 20 }} />
                                </Box>
                            ))}
                    </Block>

                    <Block title={`child runs · ${childRuns.length}`}>
                        {childRuns.length === 0
                            ? <Empty>No child runs launched yet.</Empty>
                            : childRuns.map((r) => (
                                <Box key={r.id} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
                                    <Link component="button" type="button" onClick={() => navigate(`/run/${r.id}`)} sx={{ fontWeight: 600 }}>{runLabel(r)}</Link>
                                    <Chip size="small" variant="outlined" color={STATUS_COLOR[r.status] || 'default'} label={r.status} sx={{ height: 20 }} />
                                </Box>
                            ))}
                    </Block>

                    <Block title={`PRs · ${prs.length}`}>
                        {prs.length === 0
                            ? <Empty>No PRs opened yet (final delivery opens one per repo after intent review).</Empty>
                            : prs.map((p, i) => (
                                <Box key={i} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.5 }}>
                                    <Typography variant="caption" sx={{ fontFamily: 'monospace' }}>{p.repo}</Typography>
                                    {p.url
                                        ? <Link href={p.url} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>PR <OpenInNewIcon sx={{ fontSize: 13 }} /></Link>
                                        : <Typography variant="caption" color="error">{p.err || 'failed'}</Typography>}
                                </Box>
                            ))}
                    </Block>

                    {routing.length > 0 && (
                        <Block title="review routing"><Bullets items={routing} /></Block>
                    )}

                    <Block title="final intent review">
                        {verdicts.length === 0
                            ? <Empty>Not yet reviewed.</Empty>
                            : commitments.map((c) => {
                                const v = verdictFor(c.id)
                                return (
                                    <Box key={c.id} sx={{ py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
                                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                            {v && <Chip size="small" icon={v.verdict === 'satisfied' ? <CheckCircleIcon /> : undefined} variant="outlined" color={VERDICT_COLOR[v.verdict] || 'default'} label={v.verdict} sx={{ height: 20 }} />}
                                            <Typography variant="body2">{c.statement}</Typography>
                                        </Box>
                                        {v?.evidence?.length > 0 && <Bullets items={v.evidence} />}
                                    </Box>
                                )
                            })}
                    </Block>
                </Box>
            </Box>
        </Dialog>
    )
}

export default CampaignWorkspace
