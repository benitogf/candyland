import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Link from '@mui/material/Link'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'
import Alert from '@mui/material/Alert'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'

import { candy } from '../config'
import { PHASES } from '../meta/run'
import { useAudit } from '../data/ooo'
import { StateChip } from '../components/StatusBits'

// The verification audit — the queryable record the conductor writes to
// audits/<id> when a run reaches a terminal state (status, phase, per-task
// pass/fail from each agent's last TEST emission, tokens, PR). Read-only:
// candyland observes, it does not re-derive. Null until the run finishes.
const AuditPanel = ({ run }) => {
    const audit = useAudit(run.id)

    if (!audit) {
        return (
            <Box sx={{ maxWidth: 1180, mx: 'auto', px: { xs: 2, sm: 4 }, py: 4 }}>
                <Typography variant="body2" color="text.secondary">
                    No audit yet — it's written when the run finishes (success or terminal failure).
                </Typography>
            </Box>
        )
    }

    const tasks = audit.tasks || []
    const ended = audit.endedAt ? new Date(audit.endedAt).toLocaleString() : null

    return (
        <Box sx={{ maxWidth: 1180, mx: 'auto', px: { xs: 2, sm: 4 }, py: 4 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mb: 2 }}>
                <Chip
                    size="small"
                    label={audit.status || 'unknown'}
                    color={audit.error ? 'error' : audit.status === 'done' ? 'success' : 'default'}
                    variant="outlined"
                    sx={{ fontWeight: 700 }}
                />
                <Typography variant="caption" color="text.secondary">
                    {PHASES[audit.phase] || `phase ${audit.phase}`} · {audit.tokens}k tok
                </Typography>
                {audit.prUrl && (
                    <Link href={audit.prUrl} target="_blank" rel="noopener" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5, fontSize: 13 }}>
                        PR <OpenInNewIcon sx={{ fontSize: 14 }} />
                    </Link>
                )}
                {ended && <Typography variant="caption" color="text.secondary" sx={{ ml: 'auto' }}>ended {ended}</Typography>}
            </Box>

            {audit.error && <Alert severity="error" sx={{ mb: 2 }}>{audit.error}</Alert>}

            <Card>
                <CardContent sx={{ '&:last-child': { pb: 1 } }}>
                    <Typography variant="overline" color="secondary">verification by task</Typography>
                    {tasks.length === 0 ? (
                        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>No tasks recorded for this run.</Typography>
                    ) : (
                        <Table size="small">
                            <TableHead>
                                <TableRow>
                                    <TableCell sx={{ fontWeight: 700 }}>Task</TableCell>
                                    <TableCell sx={{ fontWeight: 700 }}>State</TableCell>
                                    <TableCell sx={{ fontWeight: 700 }} align="right">Pass</TableCell>
                                    <TableCell sx={{ fontWeight: 700 }} align="right">Fail</TableCell>
                                </TableRow>
                            </TableHead>
                            <TableBody>
                                {tasks.map((t) => (
                                    <TableRow key={t.id}>
                                        <TableCell sx={{ fontFamily: 'monospace' }}>{t.id}</TableCell>
                                        <TableCell><StateChip state={t.state} /></TableCell>
                                        <TableCell align="right">{t.pass}</TableCell>
                                        <TableCell align="right" sx={{ color: t.fail > 0 ? candy.lemon : 'text.secondary', fontWeight: t.fail > 0 ? 700 : 400 }}>{t.fail}</TableCell>
                                    </TableRow>
                                ))}
                            </TableBody>
                        </Table>
                    )}
                </CardContent>
            </Card>
        </Box>
    )
}

export default AuditPanel
