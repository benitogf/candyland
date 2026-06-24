import { useEffect, useState } from 'react'
import { checkPath } from './api'

// Checks each path against the backend's filesystem (exists + readable +
// writable), re-checking whenever the set of paths changes, so the UI can flag a
// workspace folder that's gone or no longer reachable. A failed request is
// treated as "not there" rather than swallowed.
export const useFolderStatuses = (paths) => {
    const [statuses, setStatuses] = useState({})
    const key = (paths || []).join('\n')
    useEffect(() => {
        let live = true
        const list = key ? key.split('\n') : []
        Promise.all(list.map((p) => checkPath(p).then((s) => [p, s]).catch(() => [p, { path: p, exists: false }])))
            .then((pairs) => { if (live) setStatuses(Object.fromEntries(pairs)) })
        return () => { live = false }
    }, [key])
    return statuses
}

// A one-line reason a folder isn't usable, or '' when it's fine. undefined status
// (still checking) reads as fine to avoid a flash of false warnings.
export const folderIssue = (st) => {
    if (!st) return ''
    if (!st.exists) return 'missing — this folder is no longer on disk'
    if (!st.dir) return 'not a folder'
    if (!st.readable || !st.writable) return 'not readable + writable by the server'
    return ''
}
