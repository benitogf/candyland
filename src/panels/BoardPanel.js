import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Typography from '@mui/material/Typography'

import { candy } from '../config'
import { STATE_META, agentInRun, isDone } from '../mock/run'
import { StateIcon } from '../components/StatusBits'

// The per-TASK lens: every work item by status — including work with no agent
// spawned yet, and the integrate/PR steps that aren't coders. That's how it
// differs from the per-worker Agents lens.

const COLUMNS = [
    { state: 'idle', label: 'Queued' },
    { state: 'working', label: 'Building' },
    { state: 'blocked', label: 'Blocked' },
    { state: 'green', label: 'Green' },
    { state: 'integrating', label: 'Integrating' },
    { state: 'done', label: 'Done' },
]

const TaskCard = ({ task, run }) => {
    const owner = agentInRun(run, task.owner)
    const dot = (STATE_META[task.state] || STATE_META.idle).dot
    return (
        <Card sx={{ borderLeft: '3px solid', borderLeftColor: dot }}>
            <CardContent sx={{ py: 1.25, px: 1.5, '&:last-child': { pb: 1.25 } }}>
                <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.75, mb: 0.5 }}>
                    <Box sx={{ mt: '2px', flexShrink: 0 }}><StateIcon state={task.state} size={16} /></Box>
                    <Typography variant="subtitle2" sx={{ fontWeight: 700, minWidth: 0, wordBreak: 'break-word', color: isDone(task.state) ? 'text.secondary' : 'text.primary' }}>{task.title}</Typography>
                </Box>
                <Box sx={{ mb: 0.75 }}>
                    {owner
                        ? <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block' }}>{owner.emoji} {owner.role}</Typography>
                        : <Chip label="no agent yet" size="small" variant="outlined" sx={{ height: 18, fontSize: 10, color: candy.lemon, borderColor: candy.lemon }} />}
                </Box>
                <Typography variant="caption" color="text.secondary" sx={{ display: 'block', fontFamily: 'monospace', fontSize: 11, wordBreak: 'break-all' }}>
                    {task.files.join(' · ')}
                </Typography>
                {task.deps.length > 0 && (
                    <Typography variant="caption" sx={{ display: 'block', color: candy.sky, fontSize: 11, mt: 0.25 }}>
                        depends on: {task.deps.join(', ')}
                    </Typography>
                )}
            </CardContent>
        </Card>
    )
}

const BoardPanel = ({ run }) => (
    <Box>
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
            the per-task lens — every work item by status, including work with no agent yet, and the integrate/PR steps that aren't coders
        </Typography>
        <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)', lg: `repeat(${COLUMNS.length}, 1fr)` }, gap: 1.5, alignItems: 'start' }}>
            {COLUMNS.map((col) => {
                const cards = run.tasks.filter((t) => t.state === col.state)
                const m = STATE_META[col.state]
                return (
                    <Box key={col.state} sx={{ minWidth: 0 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, mb: 1, pb: 0.5, borderBottom: '2px solid', borderColor: m.dot }}>
                            <Box sx={{ width: 8, height: 8, borderRadius: '50%', backgroundColor: m.dot }} />
                            <Typography variant="overline" sx={{ color: m.color, lineHeight: 1 }}>{col.label}</Typography>
                            <Typography variant="caption" color="text.secondary">{cards.length}</Typography>
                        </Box>
                        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1, minHeight: 40 }}>
                            {cards.length === 0
                                ? <Typography variant="caption" color="text.secondary" sx={{ opacity: 0.5, pl: 0.5 }}>—</Typography>
                                : cards.map((t) => <TaskCard key={t.id} task={t} run={run} />)}
                        </Box>
                    </Box>
                )
            })}
        </Box>
    </Box>
)

export default BoardPanel
