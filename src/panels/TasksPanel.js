import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'

import MermaidDiagram from '../components/MermaidDiagram'
import { DiagramCard } from '../components/Section'
import { StateChip } from '../components/StatusBits'
import { agentInRun, isDone } from '../meta/run'

// The structural lens: the dependency DAG the conductor actually scheduled —
// built live from the real partition the tech lead emitted (run.tasks), not a
// canned example. Edges are task dependencies; the node label carries the owning
// agent and the task's current state.
const CLASS_DEFS = [
    'classDef done fill:#2a1230,stroke:#ff5fa2,color:#f4ecff;',
    'classDef green fill:#13301f,stroke:#7bdc6a,color:#d7f5cf;',
    'classDef working fill:#10283a,stroke:#56c2ff,color:#cfeaff;',
    'classDef idle fill:#211a33,stroke:#6b5c8a,color:#b8a9d8;',
    'classDef integrating fill:#102e2a,stroke:#4be3c0,color:#cdf5ee;',
    'classDef blocked fill:#332a10,stroke:#ffd93d,color:#f5ecc5;',
]
const STATE_CLASS = { idle: 'idle', working: 'working', retrying: 'working', blocked: 'blocked', integrating: 'integrating', green: 'green', done: 'done' }
// Mermaid node ids must be simple tokens; partition task ids aren't guaranteed to
// be, so namespace + sanitize them.
const nodeId = (id) => 'n_' + String(id).replace(/[^a-zA-Z0-9]/g, '_')
// Mermaid labels can't contain raw double-quotes or newlines.
const label = (s) => String(s || '').replace(/"/g, "'").replace(/\s*\n\s*/g, ' ')

// buildDag renders run.tasks as a Mermaid flowchart. Returns '' when there's no
// partition yet, so nothing fake is ever shown.
const buildDag = (run) => {
    const tasks = run.tasks || []
    if (tasks.length === 0) return ''
    const lines = ['flowchart LR']
    for (const t of tasks) {
        const owner = agentInRun(run, t.owner)
        const emoji = owner?.emoji || '⚙️'
        const role = owner?.role ? `${owner.role} · ` : ''
        lines.push(`  ${nodeId(t.id)}["${emoji} ${label(t.title)}<br/>${role}${label(t.state)}"]:::${STATE_CLASS[t.state] || 'idle'}`)
    }
    for (const t of tasks) {
        for (const d of t.deps || []) {
            if (tasks.some((x) => x.id === d)) lines.push(`  ${nodeId(d)} --> ${nodeId(t.id)}`)
        }
    }
    return [...lines, ...CLASS_DEFS.map((c) => '  ' + c)].join('\n')
}

const TasksPanel = ({ run }) => (
    <Box>
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
            the structural lens — how the feature was partitioned into fork-safe tasks, and how they depend on each other
        </Typography>

        {run.hasDag && run.tasks.length > 0 && (
            <DiagramCard caption="Edges are task dependencies. Tasks with no shared edge own disjoint files and run in parallel; integration and the PR wait for the slices they depend on.">
                <MermaidDiagram chart={buildDag(run)} />
            </DiagramCard>
        )}

        <Card sx={{ mt: run.hasDag ? 3 : 0, overflowX: 'auto' }}>
            <Table size="small" sx={{ minWidth: 640 }}>
                <TableHead>
                    <TableRow>
                        <TableCell sx={{ fontWeight: 700 }}>Task</TableCell>
                        <TableCell sx={{ fontWeight: 700 }}>Owner</TableCell>
                        <TableCell sx={{ fontWeight: 700 }}>Files (disjoint)</TableCell>
                        <TableCell sx={{ fontWeight: 700 }}>Defining test</TableCell>
                        <TableCell sx={{ fontWeight: 700 }}>State</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {run.tasks.length === 0 && (
                        <TableRow>
                            <TableCell colSpan={5} sx={{ color: 'text.secondary' }}>No tasks partitioned yet — still planning.</TableCell>
                        </TableRow>
                    )}
                    {run.tasks.map((t) => {
                        const owner = agentInRun(run, t.owner)
                        const complete = isDone(t.state)
                        return (
                            <TableRow key={t.id} sx={{ backgroundColor: complete ? 'rgba(123,220,106,0.05)' : 'transparent' }}>
                                <TableCell>
                                    <Typography variant="body2" sx={{ fontWeight: 600, color: complete ? 'text.secondary' : 'text.primary' }}>{t.title}</Typography>
                                    {t.deps.length > 0 && <Typography variant="caption" color="text.secondary">after: {t.deps.join(', ')}</Typography>}
                                </TableCell>
                                <TableCell sx={{ whiteSpace: 'nowrap', color: 'text.secondary' }}>{owner ? `${owner.emoji} ${owner.role}` : '— (queued)'}</TableCell>
                                <TableCell sx={{ fontFamily: 'monospace', fontSize: 12, color: 'text.secondary' }}>{t.files.join(', ')}</TableCell>
                                <TableCell sx={{ fontFamily: 'monospace', fontSize: 12, color: 'text.secondary' }}>{t.test}</TableCell>
                                <TableCell><StateChip state={t.state} /></TableCell>
                            </TableRow>
                        )
                    })}
                </TableBody>
            </Table>
        </Card>
    </Box>
)

export default TasksPanel
