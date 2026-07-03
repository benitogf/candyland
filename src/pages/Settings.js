import React, { useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Card from '@mui/material/Card'
import CircularProgress from '@mui/material/CircularProgress'
import MenuItem from '@mui/material/MenuItem'
import Select from '@mui/material/Select'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'

import { fetchSettings, saveSettings } from '../data/api'
import { useToast } from '../feedback'

// The curated model + thinking options — the exact enums the backend validates
// against (§9 of the contract). Labels are display-only.
const MODELS = [
    { value: 'claude-opus-4-8', label: 'Opus 4.8' },
    { value: 'claude-sonnet-5', label: 'Sonnet 5' },
    { value: 'claude-haiku-4-5-20251001', label: 'Haiku 4.5' },
]
const THINKING = ['low', 'medium', 'high']

// Roles grouped by tier — the render order and grouping of the settings table.
const TIERS = [
    { tier: 'Campaign', roles: ['intent-lead', 'intent-manager', 'intent-reviewer', 'tech-manager'] },
    { tier: 'Quest', roles: ['quest-lead'] },
    { tier: 'Run', roles: ['tech-lead', 'reviewer', 'coder', 'fix'] },
]
const ALL_ROLES = TIERS.flatMap((t) => t.roles)

// The §9 default table: every model opus-4-8; thinking low for coder+fix, high
// for all others. Reset-to-defaults restores exactly this.
const DEFAULT_LEVELS = ALL_ROLES.reduce((acc, role) => {
    acc[role] = { model: 'claude-opus-4-8', thinking: (role === 'coder' || role === 'fix') ? 'low' : 'high' }
    return acc
}, {})

// Overlay a fetched settings object onto the default table so every known role
// always has a complete {model, thinking}, even if the backend omitted a level.
const withDefaults = (levels) => ALL_ROLES.reduce((acc, role) => {
    acc[role] = { ...DEFAULT_LEVELS[role], ...(levels?.[role] || {}) }
    return acc
}, {})

const Settings = () => {
    const toast = useToast()
    const [levels, setLevels] = useState(null)
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const [saving, setSaving] = useState(false)

    useEffect(() => {
        let live = true
        fetchSettings()
            .then((cfg) => { if (live) { setLevels(withDefaults(cfg?.levels)); setLoading(false) } })
            .catch((e) => { if (live) { setError(e?.message || 'Failed to load settings'); setLevels(withDefaults(null)); setLoading(false) } })
        return () => { live = false }
    }, [])

    const setField = (role, field, value) => setLevels((prev) => ({ ...prev, [role]: { ...prev[role], [field]: value } }))

    const persist = async (next) => {
        setSaving(true)
        try {
            const saved = await saveSettings(next)
            setLevels(withDefaults(saved?.levels || next))
            toast('Settings saved')
        } catch (e) {
            toast(e?.message || 'Save failed — is the candyland server reachable?')
        } finally {
            setSaving(false)
        }
    }

    const onSave = () => persist(levels)
    const onReset = () => { setLevels(DEFAULT_LEVELS); persist(DEFAULT_LEVELS) }

    return (
        <Box>
            <Typography variant="h5" sx={{ fontWeight: 800 }}>Settings</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                Per-level agent configuration — the model and thinking effort each role is spawned with. The
                conductor reads these fresh per spawn.
            </Typography>

            {error && <Alert severity="warning" variant="outlined" sx={{ mb: 2 }}>Showing defaults — {error}</Alert>}

            {loading || !levels
                ? <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, py: 4 }}><CircularProgress size={22} /><Typography variant="body2" color="text.secondary">Loading settings…</Typography></Box>
                : (
                    <>
                        <Card sx={{ overflowX: 'auto', mb: 2 }}>
                            <Table size="small" sx={{ minWidth: 560 }}>
                                <TableHead>
                                    <TableRow>
                                        <TableCell sx={{ fontWeight: 700 }}>Role</TableCell>
                                        <TableCell sx={{ fontWeight: 700 }}>Model</TableCell>
                                        <TableCell sx={{ fontWeight: 700 }}>Thinking</TableCell>
                                    </TableRow>
                                </TableHead>
                                <TableBody>
                                    {TIERS.map((t) => (
                                        <React.Fragment key={t.tier}>
                                            <TableRow>
                                                <TableCell colSpan={3} sx={{ borderBottom: 'none', pt: 2 }}>
                                                    <Typography variant="overline" color="secondary">{t.tier}</Typography>
                                                </TableCell>
                                            </TableRow>
                                            {t.roles.map((role) => (
                                                <TableRow key={role} hover>
                                                    <TableCell sx={{ fontFamily: 'monospace' }}>{role}</TableCell>
                                                    <TableCell>
                                                        <Select
                                                            size="small" value={levels[role].model}
                                                            onChange={(e) => setField(role, 'model', e.target.value)}
                                                            sx={{ minWidth: 160 }} inputProps={{ 'aria-label': `${role} model` }}
                                                        >
                                                            {MODELS.map((m) => <MenuItem key={m.value} value={m.value}>{m.label}</MenuItem>)}
                                                        </Select>
                                                    </TableCell>
                                                    <TableCell>
                                                        <Select
                                                            size="small" value={levels[role].thinking}
                                                            onChange={(e) => setField(role, 'thinking', e.target.value)}
                                                            sx={{ minWidth: 120 }} inputProps={{ 'aria-label': `${role} thinking` }}
                                                        >
                                                            {THINKING.map((v) => <MenuItem key={v} value={v}>{v}</MenuItem>)}
                                                        </Select>
                                                    </TableCell>
                                                </TableRow>
                                            ))}
                                        </React.Fragment>
                                    ))}
                                </TableBody>
                            </Table>
                        </Card>

                        <Box sx={{ display: 'flex', gap: 1.5 }}>
                            <Button variant="contained" disabled={saving} onClick={onSave}>Save</Button>
                            <Button variant="outlined" color="secondary" disabled={saving} onClick={onReset}>Reset to defaults</Button>
                        </Box>
                    </>
                )}
        </Box>
    )
}

export default Settings
