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
import { agentInRun, isDone } from '../mock/run'

// The structural lens: the dependency DAG the conductor schedules from — how the
// tech lead partitioned the feature into fork-safe slices. Maps to the .plan
// contract. Kept as a static const so scripts/validate-diagrams.mjs parses it;
// it mirrors the csv-export deps in mock/run.js.
const DAG = `
flowchart LR
  tests["🧪 Define failing tests<br/>test eng · done"]:::done
  endpoint["⚙️ Export endpoint → CSV<br/>backend · green"]:::green
  button["🎨 Export button → download<br/>frontend · working"]:::working
  docs["📄 Document the export format<br/>queued · no agent"]:::idle
  integrate["🔗 Integrate + self-review<br/>tech lead · integrating"]:::integrating
  review["🔎 Open one PR<br/>reviewer · blocked"]:::blocked
  tests --> endpoint
  tests --> button
  endpoint --> integrate
  button --> integrate
  integrate --> review
  classDef done fill:#2a1230,stroke:#ff5fa2,color:#f4ecff;
  classDef green fill:#13301f,stroke:#7bdc6a,color:#d7f5cf;
  classDef working fill:#10283a,stroke:#56c2ff,color:#cfeaff;
  classDef idle fill:#211a33,stroke:#6b5c8a,color:#b8a9d8;
  classDef integrating fill:#102e2a,stroke:#4be3c0,color:#cdf5ee;
  classDef blocked fill:#332a10,stroke:#ffd93d,color:#f5ecc5;
`

const TasksPanel = ({ run }) => (
    <Box>
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
            the structural lens — how the feature was partitioned into fork-safe tasks, and how they depend on each other
        </Typography>

        {run.hasDag && (
            <DiagramCard caption="Edges are ordering, not file conflicts: every task owns disjoint files, so the build tasks run in parallel. The test contract comes first; integration waits for the slices; the PR waits for integration.">
                <MermaidDiagram chart={DAG} />
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
