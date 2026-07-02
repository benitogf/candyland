import React from 'react'
import Button from '@mui/material/Button'
import Link from '@mui/material/Link'
import Tooltip from '@mui/material/Tooltip'
import ContentCopyIcon from '@mui/icons-material/ContentCopy'

import { useToast } from '../feedback'

// Candyland runs from WSL and cannot open a link in the Windows browser, so a PR
// URL is COPIED to the clipboard, never opened. Falls back to surfacing the URL if
// the clipboard is unavailable (insecure context / denied permission) so the link
// is never lost. Multi-PR / multi-repo outcomes render one affordance per PR.
export const useCopyPr = () => {
    const toast = useToast()
    return (url) => (e) => {
        if (e) e.stopPropagation()
        if (!url) return
        if (!navigator.clipboard) {
            toast(`Copy unavailable here — copy manually: ${url}`)
            return
        }
        navigator.clipboard.writeText(url)
            .then(() => toast('PR link copied — paste it into your browser', 'success'))
            .catch(() => toast(`Couldn't copy — copy manually: ${url}`))
    }
}

// Full-size button (run workspace controls / terminal outcomes).
export const CopyPrButton = ({ url, label = 'PR' }) => {
    const copy = useCopyPr()
    return (
        <Tooltip title={`Copy PR link — ${url}`}>
            <Button color="secondary" variant="outlined" startIcon={<ContentCopyIcon />} onClick={copy(url)} sx={{ flexShrink: 0 }}>{label}</Button>
        </Tooltip>
    )
}

// Compact inline affordance (per-repo delivery lists).
export const CopyPrLink = ({ url, label = 'PR' }) => {
    const copy = useCopyPr()
    return (
        <Tooltip title={`Copy PR link — ${url}`}>
            <Link component="button" type="button" onClick={copy(url)} sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.3 }}>
                {label} <ContentCopyIcon sx={{ fontSize: 12 }} />
            </Link>
        </Tooltip>
    )
}
