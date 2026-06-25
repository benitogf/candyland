import React, { useState } from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import FullscreenIcon from '@mui/icons-material/Fullscreen'
import CloseIcon from '@mui/icons-material/Close'

import { candy } from '../config'
import AgentTerminal from '../components/AgentTerminal'

// The raw lens: each headless agent, its real identity, and its unparsed
// stream-json rendered in a real terminal — expand to fullscreen for room.

const sessionStatus = (state) => {
    if (state === 'working' || state === 'integrating') return { label: 'running', color: candy.sky }
    if (state === 'retrying') return { label: 'retrying', color: '#ffa94d' }
    if (state === 'blocked') return { label: 'failed', color: candy.lemon }
    if (state === 'idle') return { label: 'queued', color: candy.lemon }
    return { label: 'exited 0', color: candy.mint }
}

const rawLine = (ev) => {
    switch (ev.t) {
        case 'system': return `{"type":"system","subtype":"init","session":"${ev.text}"}`
        case 'text': return `{"type":"assistant","message":{"content":[{"type":"text","text":${JSON.stringify(ev.text)}}]}}`
        case 'tool': return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"${ev.name}","input":${JSON.stringify({ target: ev.input })}}]}}`
        case 'test': return `{"type":"user","message":{"content":[{"type":"tool_result","content":${JSON.stringify(ev.text + ' -> ' + ev.pass + ' pass, ' + ev.fail + ' fail')}}]}}`
        case 'result': return `{"type":"result","subtype":"success","result":${JSON.stringify(ev.text)}}`
        default: return ''
    }
}

// A truthful identity line from the agent's real fields — no fabricated pid or
// spawn command (canned content is a mock; the conductor owns the real launch).
const identity = (agent) => [agent.id, agent.model && `claude-${agent.model}`, agent.worktree]
    .filter(Boolean).join(' · ')

const SessionDetail = ({ agent, lines, onExpand }) => (
    <Box sx={{ minWidth: 0, height: '100%', display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 700, mb: 1, flexShrink: 0 }}>{agent.emoji} {agent.role}</Typography>
        <Box component="pre" sx={{ m: 0, mb: 2, p: 1.5, borderRadius: 1, fontSize: 11.5, lineHeight: 1.6, color: '#9b8fc0', backgroundColor: '#050506', border: '1px solid', borderColor: 'divider', overflowX: 'auto', whiteSpace: 'pre', flexShrink: 0 }}>
            {identity(agent)}
        </Box>
        <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1, flexShrink: 0 }}>
            <Typography variant="overline" color="secondary">raw stream-json · {lines.length} lines</Typography>
            <Tooltip title="Expand to full screen">
                <IconButton size="small" onClick={onExpand} aria-label="expand terminal"><FullscreenIcon fontSize="small" /></IconButton>
            </Tooltip>
        </Box>
        <Box sx={{ height: { xs: '50vh', md: 'auto' }, flex: { md: 1 }, minHeight: 240 }}>
            <AgentTerminal lines={lines} resetKey={agent.id} />
        </Box>
    </Box>
)

const SessionsPanel = ({ run }) => {
    const [selectedId, setSelectedId] = useState(run.agents[0]?.id)
    const [expanded, setExpanded] = useState(false)
    const selected = run.agents.find((a) => a.id === selectedId) || run.agents[0]
    if (!selected) return <Typography variant="body2" color="text.secondary">No sessions yet — still planning.</Typography>
    const lines = selected.events.map(rawLine).filter(Boolean)

    return (
        <Box sx={{ height: { md: '100%' }, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2, flexShrink: 0 }}>
                the raw lens — each headless agent, its real identity, and its unparsed stream-json. Expand for room.
            </Typography>
            <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '300px minmax(0, 1fr)' }, gap: 3, flex: { md: 1 }, minHeight: 0 }}>
                <Box sx={{ display: 'flex', flexDirection: { xs: 'row', md: 'column' }, gap: 1, minWidth: 0, minHeight: 0, overflowY: { md: 'auto' }, overflowX: { xs: 'auto', md: 'visible' }, pr: { md: 0.5 }, pb: { xs: 1, md: 0 }, flexShrink: 0, maxHeight: { xs: 120, md: 'none' } }}>
                    {run.agents.map((a) => {
                        const st = sessionStatus(a.state)
                        const sel = a.id === selected.id
                        return (
                            <Card key={a.id} onClick={() => setSelectedId(a.id)} sx={{ cursor: 'pointer', flexShrink: 0, minWidth: { xs: 180, md: 'auto' }, boxShadow: (t) => (sel ? `0 0 0 1px ${t.palette.primary.main}` : 'none'), '&:hover': { backgroundColor: candy.bgPaperHi } }}>
                                <CardContent sx={{ py: 1.25, '&:last-child': { pb: 1.25 } }}>
                                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, minWidth: 0 }}>
                                        <Typography variant="body2" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.emoji} {a.role}</Typography>
                                        <Box sx={{ width: 7, height: 7, borderRadius: '50%', backgroundColor: st.color, flexShrink: 0 }} />
                                        <Typography variant="caption" sx={{ color: st.color, fontFamily: 'monospace', flexShrink: 0 }}>{st.label}</Typography>
                                    </Box>
                                    <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block', fontFamily: 'monospace', fontSize: 11 }}>
                                        {a.id} · {a.tokens}k tok
                                    </Typography>
                                </CardContent>
                            </Card>
                        )
                    })}
                </Box>
                <SessionDetail agent={selected} lines={lines} onExpand={() => setExpanded(true)} />
            </Box>

            <Dialog fullScreen open={expanded} onClose={() => setExpanded(false)} aria-label="Raw session output" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, px: 2, py: 1.5, borderBottom: '1px solid', borderColor: 'divider' }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1, minWidth: 0 }} noWrap>{selected.emoji} {selected.role} · raw stream-json</Typography>
                    <IconButton onClick={() => setExpanded(false)} aria-label="close"><CloseIcon /></IconButton>
                </Box>
                <Box sx={{ flex: 1, minHeight: 0, p: 2 }}>
                    {expanded && <AgentTerminal lines={lines} resetKey={`${selected.id}-expanded`} />}
                </Box>
            </Dialog>
        </Box>
    )
}

export default SessionsPanel
