import React, { useState } from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Divider from '@mui/material/Divider'
import Typography from '@mui/material/Typography'

import { candy } from '../config'
import { STATE_META } from '../meta/run'
import { StateChip, TokenMeter } from '../components/StatusBits'

// The per-WORKER lens: who is running and what each is saying right now.
// `events` is exactly a parsed stream-json stdout — no extra source needed.

// One parsed stream-json event. Text gets a comfortable reading width and
// line-height so a long burst from an agent stays easy to scan. Tool inputs and
// result/text payloads render the COMPLETE untruncated value: the backend
// persists the full payload in inputFull/textFull whenever the compact summary
// dropped characters, so this live output view is never truncated — it falls
// back to the summary field only when the payload already fit in full.
const EventLine = ({ ev }) => {
    const mono = { fontFamily: 'monospace', fontSize: 12.5, lineHeight: 1.7, wordBreak: 'break-word', whiteSpace: 'pre-wrap' }
    const text = ev.textFull || ev.text
    const input = ev.inputFull || ev.input
    if (ev.t === 'system') return <Box sx={{ ...mono, color: candy.grape, mb: 1 }}>● {text}</Box>
    if (ev.t === 'text') return <Box sx={{ fontSize: 14, lineHeight: 1.75, color: '#e6ddff', my: 1.25, maxWidth: '74ch', whiteSpace: 'pre-wrap' }}>{text}</Box>
    if (ev.t === 'tool') {
        return (
            <Box sx={{ ...mono, my: 0.5 }}>
                <Box component="span" sx={{ color: candy.mint, fontWeight: 700 }}>⚒ {ev.name}</Box>
                {'  '}<Box component="span" sx={{ color: candy.sky }}>{input}</Box>
            </Box>
        )
    }
    if (ev.t === 'test') {
        const ok = ev.fail === 0
        return (
            <Box sx={{ ...mono, color: ok ? '#7bdc6a' : candy.lemon, my: 0.75 }}>
                {ok ? '✓' : '✗'} {ev.text} — <b>{ev.pass} pass</b>{ev.fail ? `, ${ev.fail} fail` : ''}
            </Box>
        )
    }
    if (ev.t === 'result') return <Box sx={{ ...mono, color: candy.pink, mt: 1 }}>■ {text}</Box>
    return null
}

const AgentCard = ({ agent, selected, onSelect }) => {
    const dot = (STATE_META[agent.state] || STATE_META.idle).dot
    return (
        <Card
            onClick={() => onSelect(agent.id)}
            sx={{ cursor: 'pointer', borderLeft: '3px solid', borderLeftColor: dot, boxShadow: (t) => (selected ? `0 0 0 1px ${t.palette.primary.main}` : 'none'), '&:hover': { backgroundColor: candy.bgPaperHi } }}
        >
            <CardContent sx={{ py: 1.25, '&:last-child': { pb: 1.25 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 0.5 }}>
                    <Typography variant="body1" component="span">{agent.emoji}</Typography>
                    <Typography variant="subtitle2" sx={{ fontWeight: 700, flexGrow: 1 }}>{agent.role}</Typography>
                    <StateChip state={agent.state} />
                </Box>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>{agent.activity}</Typography>
                <TokenMeter used={agent.tokens} budget={agent.budget} />
            </CardContent>
        </Card>
    )
}

const AgentDetail = ({ agent }) => (
    <Card sx={{ height: { md: '100%' }, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <CardContent sx={{ display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1, '&:last-child': { pb: 2 } }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1.5, flexShrink: 0 }}>
                <Typography variant="h6" component="span">{agent.emoji}</Typography>
                <Box sx={{ flexGrow: 1, minWidth: 0 }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 700, lineHeight: 1.2 }} noWrap>{agent.role}</Typography>
                    <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block', fontFamily: 'monospace' }}>{agent.worktree} · {agent.model}</Typography>
                </Box>
                <Box sx={{ flexShrink: 0 }}><StateChip state={agent.state} /></Box>
            </Box>
            <Box sx={{ flexShrink: 0 }}><TokenMeter used={agent.tokens} budget={agent.budget} /></Box>
            <Divider sx={{ my: 2, flexShrink: 0 }} />
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', mb: 1, flexShrink: 0 }}>
                <Typography variant="overline" color="secondary">live output · parsed from stream-json</Typography>
                <Typography variant="caption" color="text.secondary">{agent.events.length} events</Typography>
            </Box>
            <Box
                sx={{
                    p: 2.5,
                    borderRadius: 2,
                    backgroundColor: '#050506',
                    border: '1px solid',
                    borderColor: 'divider',
                    height: { xs: '50vh', md: 'auto' },
                    flex: { md: 1 },
                    minHeight: 0,
                    overflowY: 'auto',
                    overflowX: 'hidden',
                }}
            >
                {agent.events.map((ev, i) => <EventLine key={i} ev={ev} />)}
            </Box>
        </CardContent>
    </Card>
)

// The per-agent lens, reused across runs (coder fleet), quests (the quest-lead),
// and campaigns (intent-lead / intent-reviewer) — every tier records the same
// agents[].events shape via the conductor's updateAgentHost, so a campaign or
// quest's coordinating agents are as fully inspectable as a run's coders.
const AgentsPanel = ({ agents = [], emptyLabel = 'No agents spawned yet — still planning.', hint = "the per-worker lens — who's running and what each is saying. Pick an agent to read its full live output." }) => {
    const [selectedId, setSelectedId] = useState(agents[0]?.id)
    const selected = agents.find((a) => a.id === selectedId) || agents[0]
    if (!selected) return <Typography variant="body2" color="text.secondary">{emptyLabel}</Typography>

    return (
        <Box sx={{ height: { md: '100%' }, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2, flexShrink: 0 }}>
                {hint}
            </Typography>
            <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '300px minmax(0, 1fr)' }, gap: 3, flex: { md: 1 }, minHeight: 0 }}>
                <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.25, minWidth: 0, minHeight: 0, overflowY: { md: 'auto' }, pr: { md: 0.5 } }}>
                    <Typography variant="overline" color="secondary" sx={{ flexShrink: 0 }}>the fleet · {agents.length}</Typography>
                    {agents.map((a) => (
                        <AgentCard key={a.id} agent={a} selected={a.id === selected.id} onSelect={setSelectedId} />
                    ))}
                </Box>
                <AgentDetail agent={selected} />
            </Box>
        </Box>
    )
}

export default AgentsPanel
