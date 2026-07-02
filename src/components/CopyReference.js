import React from 'react'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import LinkIcon from '@mui/icons-material/Link'

import { useToast } from '../feedback'
import { referenceText } from '../lib/reference'

// One-click copy of a task/quest/campaign reference — a stable handle plus a
// resolvable API URL — ready to paste into a VSCode Claude session. Lives inside
// clickable Work-list rows, so it stops click propagation to avoid navigating
// into the item, and never fails silently: a clipboard rejection (denied
// permission, insecure context) surfaces the reference so it can be copied by hand.
const CopyReference = ({ kind, id, size = 15 }) => {
    const toast = useToast()
    const copy = (e) => {
        e.stopPropagation()
        const text = referenceText(kind, id)
        if (!navigator.clipboard) {
            toast(`Copy unavailable here — copy the reference manually: ${text}`)
            return
        }
        navigator.clipboard.writeText(text)
            .then(() => toast('Reference copied — paste it into a Claude session', 'success'))
            .catch(() => toast(`Couldn't copy — copy the reference manually: ${text}`))
    }
    return (
        <Tooltip title="Copy reference">
            <IconButton size="small" onClick={copy} aria-label="copy reference">
                <LinkIcon sx={{ fontSize: size }} />
            </IconButton>
        </Tooltip>
    )
}

export default CopyReference
