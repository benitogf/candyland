import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

// Global resets so the dashboard fills the viewport.
const style = document.createElement('style')
style.textContent = `
    body {
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Roboto", "Oxygen",
            "Ubuntu", "Cantarell", "Fira Sans", "Droid Sans", "Helvetica Neue",
            sans-serif;
        -webkit-font-smoothing: antialiased;
        -moz-osx-font-smoothing: grayscale;
    }

    body,
    html,
    #root {
        height: 100%;
        width: 100%;
        margin: 0;
        padding: 0;
    }
`
document.head.appendChild(style)

const root = ReactDOM.createRoot(document.getElementById('root'))
root.render(
    <React.StrictMode>
        <App />
    </React.StrictMode>
)
