import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Typography from '@mui/material/Typography'

// Read-only audit surfaces for the detail views: the schema-valid Postmortem a
// terminal `blocked` carries (§3), and the recorded upward decision-escalation
// audit trail (§2). Both render nothing when absent, and neither offers any
// action — they exist purely for audit. Field names mirror internal/run/types.go
// (Postmortem / Escalation JSON tags) exactly.

const Label = ({ children }) => (
    <Typography variant="caption" color="text.secondary" sx={{ display: 'block', textTransform: 'uppercase', letterSpacing: 0.4, mt: 1.25 }}>{children}</Typography>
)

// The terminal blocked explanation. Rendered only when a postmortem is present
// (i.e. the record terminated blocked). Preserves whitespace in the free-text
// fields so verbatim evidence/commands read as captured.
export const PostmortemBlock = ({ postmortem }) => {
    if (!postmortem) return null
    const pm = postmortem
    return (
        <Card sx={{ mb: 2.5, borderLeft: '3px solid', borderColor: 'warning.main' }}>
            <CardContent>
                <Typography variant="overline" color="warning.main" sx={{ display: 'block', mb: 1 }}>postmortem · blocked (capability failure)</Typography>

                {pm.failingCapability && (
                    <>
                        <Label>failing capability</Label>
                        <Typography variant="body2">{pm.failingCapability}</Typography>
                    </>
                )}

                {(pm.attempts || []).length > 0 && (
                    <>
                        <Label>attempts</Label>
                        <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
                            {pm.attempts.map((a, i) => <Typography key={i} component="li" variant="body2" color="text.secondary">{a}</Typography>)}
                        </Box>
                    </>
                )}

                {pm.evidence && (
                    <>
                        <Label>evidence</Label>
                        <Typography variant="body2" color="text.secondary" component="pre" sx={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', m: 0, fontFamily: 'monospace', fontSize: 12 }}>{pm.evidence}</Typography>
                    </>
                )}

                {pm.rootCauseSoFar && (
                    <>
                        <Label>root cause so far</Label>
                        <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap' }}>{pm.rootCauseSoFar}</Typography>
                    </>
                )}

                {pm.humanUnblockAction && (
                    <>
                        <Label>human unblock action</Label>
                        <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>{pm.humanUnblockAction}</Typography>
                    </>
                )}

                {pm.partialWorkState && (
                    <>
                        <Label>partial work state</Label>
                        <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{pm.partialWorkState}</Typography>
                    </>
                )}
            </CardContent>
        </Card>
    )
}

// The recorded decision-escalation audit trail. Each entry: from→to, the
// question, the decider (lowest tier with authority), and the recorded answer.
export const EscalationsBlock = ({ escalations }) => {
    const list = escalations || []
    if (list.length === 0) return null
    return (
        <Card sx={{ mb: 2.5 }}>
            <CardContent>
                <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 1 }}>escalations · {list.length}</Typography>
                {list.map((e, i) => (
                    <Box key={i} sx={{ py: 1, borderBottom: i < list.length - 1 ? '1px solid' : 'none', borderColor: 'divider' }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 0.5 }}>
                            <Chip size="small" variant="outlined" label={`${e.from || '—'} → ${e.to || '—'}`} sx={{ height: 20, fontFamily: 'monospace' }} />
                            {e.decider && <Typography variant="caption" color="text.secondary">decided by {e.decider}</Typography>}
                        </Box>
                        {e.question && <Typography variant="body2"><b>Q:</b> {e.question}</Typography>}
                        {e.answer && <Typography variant="body2" color="text.secondary"><b>A:</b> {e.answer}</Typography>}
                    </Box>
                ))}
            </CardContent>
        </Card>
    )
}
