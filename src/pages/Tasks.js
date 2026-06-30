import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import InputAdornment from '@mui/material/InputAdornment'
import Link from '@mui/material/Link'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import SearchIcon from '@mui/icons-material/Search'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'

import { PHASES } from '../meta/run'
import { runLabel } from '../util'
import { useRuns } from '../data/ooo'

// The full history of every run, in any state — including ones cleared from the
// dashboard. Searchable; a row opens the run. The dashboard stays focused on
// recent, non-archived runs; this is the complete record.

const STATUS_COLOR = { done: 'success', cancelled: 'default', paused: 'warning', running: 'info', planning: 'secondary' }
const statusText = (r) => {
    if (r.status === 'running') return PHASES[r.phase] || 'Running'
    return r.status ? r.status.charAt(0).toUpperCase() + r.status.slice(1) : '—'
}

const Tasks = () => {
    const navigate = useNavigate()
    const runs = useRuns() // all runs, including archived — this is the history
    const folderOf = (r) => r.folders?.[0] || '—'
    const [query, setQuery] = useState('')

    const needle = query.trim().toLowerCase()
    const matches = (r) => !needle || [runLabel(r), r.status, folderOf(r), r.prompt, r.branch].some((s) => String(s || '').toLowerCase().includes(needle))
    const filtered = runs.filter(matches)

    return (
        <Box>
            <Typography variant="h5" sx={{ fontWeight: 800 }}>Tasks</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>Every run, in any state — including ones cleared from the dashboard.</Typography>

            <TextField
                size="small" fullWidth placeholder="Search by title, prompt, folder, status…"
                value={query} onChange={(e) => setQuery(e.target.value)} sx={{ mb: 2, maxWidth: 520 }}
                InputProps={{ startAdornment: <InputAdornment position="start"><SearchIcon fontSize="small" /></InputAdornment> }}
            />

            <Card sx={{ overflowX: 'auto' }}>
                <Table size="small" sx={{ minWidth: 720 }}>
                    <TableHead>
                        <TableRow>
                            <TableCell sx={{ fontWeight: 700 }}>Task</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>Status</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>Folder</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>PR</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {filtered.length === 0 && (
                            <TableRow>
                                <TableCell colSpan={4} sx={{ color: 'text.secondary' }}>
                                    {runs.length === 0 ? 'No runs yet — start one from the dashboard.' : 'No runs match your search.'}
                                </TableCell>
                            </TableRow>
                        )}
                        {filtered.map((r) => (
                            <TableRow
                                key={r.id} hover onClick={() => navigate(`/run/${r.id}`)}
                                sx={{ cursor: 'pointer', opacity: r.archived ? 0.6 : 1 }}
                            >
                                <TableCell>
                                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                                        <Typography variant="body2" sx={{ fontWeight: 600 }}>{runLabel(r)}</Typography>
                                        {r.archived && <Chip size="small" variant="outlined" label="cleared" sx={{ height: 18, fontSize: 10 }} />}
                                    </Box>
                                </TableCell>
                                <TableCell><Chip size="small" variant="outlined" color={STATUS_COLOR[r.status] || 'default'} label={statusText(r)} sx={{ height: 22 }} /></TableCell>
                                <TableCell>
                                    <Typography
                                        variant="body2" component="span"
                                        title={folderOf(r)}
                                        sx={{ color: 'text.secondary', fontFamily: 'monospace', fontSize: 12, maxWidth: 260, display: 'inline-block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', verticalAlign: 'bottom' }}
                                    >
                                        {folderOf(r)}
                                    </Typography>
                                </TableCell>
                                <TableCell onClick={(e) => e.stopPropagation()}>
                                    {r.prUrl
                                        ? <Link href={r.prUrl} target="_blank" rel="noreferrer" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>PR <OpenInNewIcon sx={{ fontSize: 13 }} /></Link>
                                        : <Typography variant="caption" color="text.secondary">—</Typography>}
                                </TableCell>
                            </TableRow>
                        ))}
                    </TableBody>
                </Table>
            </Card>
        </Box>
    )
}

export default Tasks
