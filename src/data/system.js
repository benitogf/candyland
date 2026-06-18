import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { fetchSystem } from './api'

// Polls /api/system for the detected platform, dependency state, and executor
// mode. Doubles as the backend reachability check: a failed fetch means the
// server is unreachable, which the UI surfaces with start instructions and uses
// to disable actions that would just fail.
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

// One shared poller for the whole app, so every component reads the same
// reachability/system state (and we don't fan out N pollers).
const SystemContext = createContext({ system: null, reachable: true, loading: true, refetch: () => {} })

export const SystemProvider = ({ children }) => {
    const value = useSystem()
    return <SystemContext.Provider value={value}>{children}</SystemContext.Provider>
}

export const useSystemStatus = () => useContext(SystemContext)
