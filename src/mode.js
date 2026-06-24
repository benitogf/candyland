import { createContext, useContext } from 'react'

// The active mode colors the whole app. The dashboard toggle sets it; opening a
// run sets it to that run's mode. Held at the App root so the theme can react.
export const ModeContext = createContext({ mode: 'non-developer', setMode: () => {} })

export const useMode = () => useContext(ModeContext)
