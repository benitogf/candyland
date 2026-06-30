import React from 'react'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import InputAdornment from '@mui/material/InputAdornment'
import MenuItem from '@mui/material/MenuItem'
import TextField from '@mui/material/TextField'
import SearchIcon from '@mui/icons-material/Search'

import { noFilters, folderOf } from '../data/filters'

// The shared filter row for the work/history section. The SAME filters apply at
// every level (runs · quests · campaigns) and persist across a pivot — they live
// in the URL query string (see Tasks.js). Each control writes one query key via
// onChange(key, value); an empty value clears that key.
//
// Filters: text · no-parent / by-campaign / by-quest (parent) · by-repo/folder ·
// by-status · by-PR-state · by-date (from/to).

// The status options the active level can actually take. Runs use the lifecycle
// + planning; quests/campaigns add paused/stopped/blocked.
const STATUS_OPTIONS = {
    runs: ['planning', 'running', 'paused', 'done', 'cancelled'],
    quests: ['running', 'paused', 'stopped', 'blocked', 'done'],
    campaigns: ['running', 'paused', 'stopped', 'blocked', 'done'],
}

const Select = ({ label, value, onChange, width = 150, children }) => (
    <TextField
        select size="small" label={label} value={value} onChange={(e) => onChange(e.target.value)}
        sx={{ minWidth: width }} SelectProps={{ displayEmpty: true }}
    >
        {children}
    </TextField>
)

const FilterBar = ({ level, filters, runs, quests, campaigns, onChange, onClear }) => {
    // Parent options depend on the level: runs can be filtered by a campaign or a
    // quest; quests by a campaign; campaigns have no parent (the control hides).
    const campaignOpts = campaigns.map((c) => c.id)
    const questOpts = quests.map((q) => q.id)
    const showParent = level !== 'campaigns'

    // Distinct repos/folders across the active level, for the repo dropdown.
    const source = level === 'runs' ? runs : level === 'quests' ? quests : campaigns
    const repos = [...new Set(source.map(folderOf).filter(Boolean))]

    return (
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1.5, alignItems: 'center', mb: 2 }}>
            <TextField
                size="small" placeholder="Search…" value={filters.q}
                onChange={(e) => onChange('q', e.target.value)} sx={{ minWidth: 240, flexGrow: 1, maxWidth: 360 }}
                InputProps={{ startAdornment: <InputAdornment position="start"><SearchIcon fontSize="small" /></InputAdornment> }}
            />

            {showParent && (
                <Select label="Parent" value={filters.parent} onChange={(v) => onChange('parent', v)} width={170}>
                    <MenuItem value="">Any parent</MenuItem>
                    <MenuItem value="none">No parent</MenuItem>
                    {campaignOpts.length > 0 && <MenuItem disabled value="__c">— campaigns —</MenuItem>}
                    {campaignOpts.map((id) => <MenuItem key={id} value={id}>{id}</MenuItem>)}
                    {level === 'runs' && questOpts.length > 0 && <MenuItem disabled value="__q">— quests —</MenuItem>}
                    {level === 'runs' && questOpts.map((id) => <MenuItem key={id} value={id}>{id}</MenuItem>)}
                </Select>
            )}

            {repos.length > 0 && (
                <Select label="Repo" value={filters.repo} onChange={(v) => onChange('repo', v)} width={180}>
                    <MenuItem value="">Any repo</MenuItem>
                    {repos.map((r) => <MenuItem key={r} value={r} sx={{ fontFamily: 'monospace', fontSize: 12 }}>{r.split('/').pop() || r}</MenuItem>)}
                </Select>
            )}

            <Select label="Status" value={filters.status} onChange={(v) => onChange('status', v)} width={140}>
                <MenuItem value="">Any status</MenuItem>
                {(STATUS_OPTIONS[level] || []).map((s) => <MenuItem key={s} value={s}>{s}</MenuItem>)}
            </Select>

            <Select label="PR" value={filters.pr} onChange={(v) => onChange('pr', v)} width={120}>
                <MenuItem value="">Any PR</MenuItem>
                <MenuItem value="open">Has PR</MenuItem>
                <MenuItem value="none">No PR</MenuItem>
            </Select>

            <TextField
                size="small" type="date" label="From" value={filters.from}
                onChange={(e) => onChange('from', e.target.value)} InputLabelProps={{ shrink: true }} sx={{ width: 160 }}
            />
            <TextField
                size="small" type="date" label="To" value={filters.to}
                onChange={(e) => onChange('to', e.target.value)} InputLabelProps={{ shrink: true }} sx={{ width: 160 }}
            />

            {(!noFilters(filters) || filters.q) && (
                <Button size="small" color="inherit" onClick={onClear}>Clear</Button>
            )}
        </Box>
    )
}

export default FilterBar
