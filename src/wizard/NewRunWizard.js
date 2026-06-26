import React, { useRef, useState } from 'react'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Dialog from '@mui/material/Dialog'
import IconButton from '@mui/material/IconButton'
import Step from '@mui/material/Step'
import StepLabel from '@mui/material/StepLabel'
import Stepper from '@mui/material/Stepper'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CloseIcon from '@mui/icons-material/Close'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import ArrowForwardIcon from '@mui/icons-material/ArrowForward'
import RocketLaunchIcon from '@mui/icons-material/RocketLaunch'
import UploadFileIcon from '@mui/icons-material/UploadFile'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked'

import { useMode } from '../mode'
import { MODES } from '../meta/run'
import { useSystemStatus } from '../data/system'
import { suggestTitle } from '../util'
import CommandInput from '../components/CommandInput'

const STEPS = ['Mode', 'Folder', 'Prompt']

// One focused decision per screen — a guided walk, not a control panel. Sets the
// app mode (recolors), takes the repository folder, then the prompt (multiline /
// .md upload) with an optional, auto-suggested title. Back/edit at every step.
// This is the SECONDARY entry: most runs launch from the editor via the
// candyland MCP (which uses the editor's cwd); here you name the repo by path.
const SelectCard = ({ selected, onClick, accent, children }) => (
    <Card
        onClick={onClick}
        sx={{
            p: 2, cursor: 'pointer', display: 'flex', alignItems: 'flex-start', gap: 1.5,
            boxShadow: selected ? `0 0 0 1px ${accent}` : 'none',
            backgroundColor: selected ? `${accent}14` : undefined,
            '&:hover': { backgroundColor: 'rgba(255,255,255,0.04)' },
        }}
    >
        {selected ? <CheckCircleIcon sx={{ color: accent }} /> : <RadioButtonUncheckedIcon sx={{ color: 'text.disabled' }} />}
        <Box sx={{ minWidth: 0 }}>{children}</Box>
    </Card>
)

const NewRunWizard = ({ onClose, onStart }) => {
    const { mode, setMode } = useMode()
    const { reachable } = useSystemStatus()
    const [step, setStep] = useState(0)
    const [folder, setFolder] = useState('')
    const [prompt, setPrompt] = useState('')
    const [title, setTitle] = useState('')
    const fileRef = useRef(null)

    const canNext = step === 0 ? !!mode : step === 1 ? folder.trim().length > 0 : prompt.trim().length > 0
    const next = () => (step < STEPS.length - 1 ? setStep(step + 1) : onStart({ folders: [folder.trim()], prompt: prompt.trim(), title: title.trim() }))
    const back = () => (step === 0 ? onClose() : setStep(step - 1))

    const onFile = (e) => {
        const f = e.target.files?.[0]
        if (!f) return
        const r = new FileReader()
        r.onload = () => setPrompt(String(r.result || ''))
        r.readAsText(f)
        e.target.value = ''
    }

    const suggested = suggestTitle(prompt)

    return (
        <Dialog fullScreen open onClose={onClose} aria-label="Start a new run" PaperProps={{ sx: { backgroundColor: 'background.default', backgroundImage: 'none', display: 'flex', flexDirection: 'column' } }}>
            {/* Header */}
            <Box sx={{ borderBottom: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, py: 2 }}>
                <Box sx={{ maxWidth: 720, mx: 'auto', display: 'flex', alignItems: 'center', gap: 2 }}>
                    <Typography variant="h6" sx={{ fontWeight: 800, flexGrow: 1 }}>Start a new run</Typography>
                    <IconButton onClick={onClose} aria-label="close"><CloseIcon /></IconButton>
                </Box>
                <Box sx={{ maxWidth: 480, mx: 'auto', mt: 1 }}>
                    <Stepper activeStep={step} alternativeLabel>
                        {STEPS.map((s) => <Step key={s}><StepLabel>{s}</StepLabel></Step>)}
                    </Stepper>
                </Box>
            </Box>

            {/* Body — one step, centered and focused */}
            <Box sx={{ flexGrow: 1, overflowY: 'auto', overflowX: 'hidden' }}>
                <Box sx={{ maxWidth: 620, mx: 'auto', px: { xs: 2, sm: 4 }, py: 4 }}>
                    {step === 0 && (
                        <>
                            <Typography variant="h4" sx={{ mb: 0.5 }}>How do you want to work?</Typography>
                            <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>This sets how much detail you'll see — you can't get it wrong.</Typography>
                            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.5 }}>
                                {Object.entries(MODES).map(([key, m]) => (
                                    <SelectCard key={key} selected={mode === key} accent={m.accent} onClick={() => setMode(key)}>
                                        <Typography variant="subtitle1" sx={{ fontWeight: 800, color: mode === key ? m.accent : 'text.primary' }}>{m.label}</Typography>
                                        <Typography variant="body2" color="text.secondary">{m.tagline}</Typography>
                                    </SelectCard>
                                ))}
                            </Box>
                        </>
                    )}

                    {step === 1 && (
                        <>
                            <Typography variant="h4" sx={{ mb: 0.5 }}>Which repository?</Typography>
                            <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
                                The absolute path to the git repo this run works in — it branches and opens its PR there.
                                (Launching from your editor instead? The candyland MCP uses your current folder automatically.)
                            </Typography>
                            <TextField
                                fullWidth autoFocus
                                label="Repository folder"
                                value={folder}
                                onChange={(e) => setFolder(e.target.value)}
                                placeholder="/home/you/code/your-repo"
                                helperText="An absolute path on the machine running candyland."
                                InputProps={{ sx: { fontFamily: 'monospace' } }}
                            />
                        </>
                    )}

                    {step === 2 && (
                        <>
                            <Typography variant="h4" sx={{ mb: 0.5 }}>What do you want done?</Typography>
                            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                                Describe it in plain language — as long as you like. Type <Box component="span" sx={{ fontFamily: 'monospace', color: 'primary.main' }}>/</Box> to reference a detritus command, or upload a brief.
                            </Typography>
                            <CommandInput fullWidth multiline minRows={6} autoFocus placeholder={'e.g. Add a CSV export to the reports page so people can download what they\'re viewing.'} value={prompt} onChange={setPrompt} />
                            <Box sx={{ display: 'flex', gap: 1, mt: 1, flexWrap: 'wrap' }}>
                                <Button size="small" variant="outlined" startIcon={<UploadFileIcon />} onClick={() => fileRef.current?.click()}>Upload .md</Button>
                                <input ref={fileRef} type="file" accept=".md,.markdown,.txt" hidden onChange={onFile} />
                            </Box>
                            <TextField
                                fullWidth size="small" sx={{ mt: 3 }}
                                label="Title (optional)"
                                value={title}
                                onChange={(e) => setTitle(e.target.value)}
                                placeholder={suggested || "Optional — we'll name it for you"}
                                helperText={title.trim() ? 'Used as the label for this run.' : suggested ? `We'll call it "${suggested}" unless you set one. The title isn't sent to the agent.` : "The title is just a label — it isn't sent to the agent."}
                            />
                        </>
                    )}
                </Box>
            </Box>

            {/* Footer — Back / Next|Start */}
            <Box sx={{ borderTop: '1px solid', borderColor: 'divider', px: { xs: 2, sm: 4 }, py: 2 }}>
                <Box sx={{ maxWidth: 620, mx: 'auto', display: 'flex', alignItems: 'center' }}>
                    <Button startIcon={<ArrowBackIcon />} color="inherit" onClick={back}>{step === 0 ? 'Cancel' : 'Back'}</Button>
                    <Box sx={{ flexGrow: 1 }} />
                    {step < STEPS.length - 1
                        ? <Button variant="contained" endIcon={<ArrowForwardIcon />} disabled={!canNext} onClick={next}>Next</Button>
                        : (
                            <Tooltip title={reachable ? '' : 'Server unreachable — start ./candyland first'} disableHoverListener={reachable}>
                                <Box component="span">
                                    <Button variant="contained" startIcon={<RocketLaunchIcon />} disabled={!canNext || !reachable} onClick={next}>Start run</Button>
                                </Box>
                            </Tooltip>
                        )}
                </Box>
            </Box>
        </Dialog>
    )
}

export default NewRunWizard
