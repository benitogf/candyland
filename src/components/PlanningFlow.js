import React, { useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import LinearProgress from '@mui/material/LinearProgress'
import Typography from '@mui/material/Typography'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked'
import ArrowForwardIcon from '@mui/icons-material/ArrowForward'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch'

import CommandInput from './CommandInput'
import { fetchQuestions } from '../data/api'

// The planning Q&A. Questions are fetched from the backend (a real request, with
// a loading state while they arrive), then each question shows a brief "thinking"
// state. Non-developer = multiple-choice; developer = open-ended with slash
// autocomplete and an explicit "build it" go-gate. Ends with onComplete(answers).
const THINK_MS = 900

const OptionCard = ({ label, selected, multi, onClick, disabled }) => (
    <Card
        onClick={disabled ? undefined : onClick}
        sx={{
            px: 2, py: 1.5, cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.5 : 1,
            display: 'flex', alignItems: 'center', gap: 1.5,
            transition: 'background-color 120ms, box-shadow 120ms',
            boxShadow: (t) => (selected ? `0 0 0 1px ${t.palette.primary.main}` : 'none'),
            backgroundColor: (t) => (selected ? `${t.palette.primary.main}14` : undefined),
            '&:hover': { backgroundColor: disabled ? undefined : 'rgba(255,255,255,0.04)' },
        }}
    >
        {multi
            ? (selected ? <CheckCircleIcon color="primary" /> : <RadioButtonUncheckedIcon sx={{ color: 'text.disabled' }} />)
            : <RadioButtonUncheckedIcon sx={{ color: selected ? 'primary.main' : 'text.disabled' }} />}
        <Typography variant="body1" sx={{ fontWeight: selected ? 700 : 500 }}>{label}</Typography>
    </Card>
)

const Loader = ({ text }) => (
    <Box sx={{ maxWidth: 640, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2, py: 6 }}>
        <CircularProgress size={22} />
        <Typography variant="body1" color="text.secondary">{text}</Typography>
    </Box>
)

const PlanningFlow = ({ runId, mode, onComplete, onError, reachable = true }) => {
    const dev = mode === 'developer'
    const [questions, setQuestions] = useState(null)
    const [error, setError] = useState(false)
    const [reload, setReload] = useState(0)
    const [step, setStep] = useState(0)
    const [answers, setAnswers] = useState({})
    const [draft, setDraft] = useState('')
    const [picks, setPicks] = useState([])
    const [loading, setLoading] = useState(true)

    // Generate the planning questions for THIS run from its prompt (backend asks
    // Claude). A failure (server unreachable / error) surfaces an actionable
    // retry — never a silent empty flow that looks like a legitimate
    // no-questions result. An empty array IS legitimate (skip straight to build).
    useEffect(() => {
        if (!runId) return undefined
        let live = true
        setError(false)
        setQuestions(null)
        fetchQuestions(runId)
            .then((qs) => { if (live) setQuestions(qs) })
            .catch(() => {
                if (!live) return
                setError(true)
                if (onError) onError()
            })
        return () => { live = false }
    }, [runId, reload]) // eslint-disable-line react-hooks/exhaustive-deps

    const q = questions ? questions[step] : null

    // Brief "thinking" state before each question appears.
    useEffect(() => {
        if (!questions) return undefined
        setLoading(true)
        const id = setTimeout(() => setLoading(false), THINK_MS)
        return () => clearTimeout(id)
    }, [step, questions])

    // Restore any previously-given answer when revisiting a step.
    useEffect(() => {
        if (!q) return
        const a = answers[q.id]
        setDraft(typeof a === 'string' ? a : '')
        setPicks(Array.isArray(a) ? a : [])
    }, [step, questions]) // eslint-disable-line react-hooks/exhaustive-deps

    if (error) {
        return (
            <Box sx={{ maxWidth: 640, mx: 'auto', py: 6 }}>
                <Alert
                    severity="error"
                    action={<Button color="inherit" size="small" onClick={() => setReload((n) => n + 1)}>Retry</Button>}
                >
                    Couldn&apos;t load the planning questions — is the candyland server reachable? Start it with <Box component="span" sx={{ fontFamily: 'monospace' }}>./candyland</Box>, then retry.
                </Alert>
            </Box>
        )
    }
    if (!questions) return <Loader text="Preparing your questions…" />
    if (questions.length === 0) {
        return (
            <Box sx={{ maxWidth: 640, mx: 'auto', textAlign: 'center', py: 6 }}>
                {!reachable && (
                    <Alert severity="error" variant="outlined" sx={{ mb: 2, textAlign: 'left' }}>
                        Server unreachable — start <Box component="span" sx={{ fontFamily: 'monospace' }}>./candyland</Box> to begin the build.
                    </Alert>
                )}
                <Button variant="contained" startIcon={<RocketLaunchIcon />} disabled={!reachable} onClick={() => onComplete({})}>Start building</Button>
            </Box>
        )
    }

    const isLast = step === questions.length - 1
    const commit = (value) => {
        if (isLast && !reachable) return // beginning the build needs the server; the banner explains why
        const next = { ...answers, [q.id]: value }
        setAnswers(next)
        if (isLast) onComplete(next)
        else setStep(step + 1)
    }
    const toggle = (label) => setPicks((p) => (p.includes(label) ? p.filter((x) => x !== label) : [...p, label]))
    // The answer UI is driven by whether the (generated) question offers options,
    // not by the mode — so a question is always answerable however Claude shaped it.
    const hasOptions = (q.options?.length ?? 0) > 0
    const canContinue = hasOptions ? (q.multi ? picks.length > 0 : true) : draft.trim().length > 0

    return (
        <Box sx={{ maxWidth: 640, mx: 'auto' }}>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>
                {dev ? 'Settle the plan — answer as much as you like; you approve before the build.' : 'A few quick questions so we build the right thing.'}
            </Typography>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2 }}>
                <Typography variant="overline" color="primary" sx={{ whiteSpace: 'nowrap' }}>Question {step + 1} of {questions.length}</Typography>
                <LinearProgress variant="determinate" value={(step / questions.length) * 100} sx={{ flexGrow: 1, height: 5, borderRadius: 3 }} />
            </Box>

            {!reachable && (
                <Alert severity="error" variant="outlined" sx={{ mb: 2 }}>
                    Server unreachable — start <Box component="span" sx={{ fontFamily: 'monospace' }}>./candyland</Box> to begin the build. You can keep answering; the build unlocks when it&apos;s back.
                </Alert>
            )}

            {loading ? (
                <Loader text={step === 0 ? 'Preparing the first question…' : 'Preparing the next question…'} />
            ) : (
                <>
                    <Typography variant="h5" sx={{ fontWeight: 800, mb: 2 }}>{q.question}</Typography>

                    {hasOptions ? (
                        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.25 }}>
                            {q.options.map((opt) => (
                                <OptionCard key={opt} label={opt} multi={q.multi} disabled={isLast && !q.multi && !reachable} selected={q.multi ? picks.includes(opt) : answers[q.id] === opt} onClick={() => (q.multi ? toggle(opt) : commit(opt))} />
                            ))}
                        </Box>
                    ) : (
                        <Box>
                            <CommandInput fullWidth multiline minRows={3} autoFocus placeholder={q.placeholder} value={draft} onChange={setDraft} />
                            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.75 }}>
                                Tip: type <Box component="span" sx={{ fontFamily: 'monospace', color: 'primary.main' }}>/</Box> to reference a detritus command.
                            </Typography>
                            {q.suggestions?.length > 0 && (
                                <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', mt: 1 }}>
                                    {q.suggestions.map((s) => (
                                        <Chip key={s} label={`+ ${s}`} size="small" variant="outlined" onClick={() => setDraft((d) => (d ? `${d}, ${s}` : s))} sx={{ cursor: 'pointer' }} />
                                    ))}
                                </Box>
                            )}
                        </Box>
                    )}

                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, mt: 3 }}>
                        <Button disabled={step === 0} startIcon={<ArrowBackIcon />} color="inherit" onClick={() => setStep(step - 1)}>Back</Button>
                        <Box sx={{ flexGrow: 1 }} />
                        {(!hasOptions || q.multi) && (
                            isLast
                                ? <Button variant="contained" startIcon={<RocketLaunchIcon />} disabled={!canContinue || !reachable} onClick={() => commit(hasOptions ? picks : draft.trim())}>{dev ? 'Looks good — build it' : 'Start building'}</Button>
                                : <Button variant="contained" endIcon={<ArrowForwardIcon />} disabled={!canContinue} onClick={() => commit(hasOptions ? picks : draft.trim())}>Continue</Button>
                        )}
                    </Box>
                    {hasOptions && !q.multi && (
                        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>Pick one to continue.</Typography>
                    )}
                </>
            )}
        </Box>
    )
}

export default PlanningFlow
