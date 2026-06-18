import React, { createContext, useCallback, useContext, useState } from 'react'
import Snackbar from '@mui/material/Snackbar'
import Alert from '@mui/material/Alert'

// App-wide toast. Backend/REST failures surface here with a clear message
// instead of failing silently. useToast()(message, severity?).
const ToastContext = createContext(() => {})

export const useToast = () => useContext(ToastContext)

export const ToastProvider = ({ children }) => {
    const [toast, setToast] = useState(null)
    const show = useCallback((message, severity = 'error') => setToast({ message, severity, key: Date.now() }), [])

    return (
        <ToastContext.Provider value={show}>
            {children}
            <Snackbar
                open={!!toast}
                autoHideDuration={7000}
                onClose={() => setToast(null)}
                anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
            >
                {toast ? (
                    <Alert severity={toast.severity} variant="filled" onClose={() => setToast(null)} sx={{ maxWidth: 520 }}>
                        {toast.message}
                    </Alert>
                ) : undefined}
            </Snackbar>
        </ToastContext.Provider>
    )
}
