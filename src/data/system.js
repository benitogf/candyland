import { useCallback, useEffect, useState } from 'react'
import { fetchSystem } from './api'

// Polls /api/system for the detected platform, dependency state, and executor
// mode. Doubles as the backend reachability check: a failed fetch means the
// server is unreachable, which the UI surfaces with start instructions.
export const useSystem = () => {
    const [system, setSystem] = useState(null)
    const [reachable, setReachable] = useState(true)
    const [loading, setLoading] = useState(true)

    const load = useCallback(async () => {
        try {
            const d = await fetchSystem()
            setSystem(d)
            setReachable(true)
        } catch {
            setReachable(false)
        } finally {
            setLoading(false)
        }
    }, [])

    useEffect(() => {
        load()
        const t = setInterval(load, 8000)
        return () => clearInterval(t)
    }, [load])

    return { system, reachable, loading, refetch: load }
}
