import React from 'react'
import ReactDOM from 'react-dom/client'
// Self-hosted color-emoji font (bundled woff2, no CDN). Candyland's packaged
// webview (WSL/Linux) has no guaranteed system emoji font, so the literal emojis
// across the UI (agent emojis, 🍬, chips) rendered as tofu squares. Loading this
// and listing "Noto Color Emoji" as the last family in every stack gives those
// codepoints a guaranteed glyph.
import '@fontsource/noto-color-emoji/index.css'
import App from './App'

// Global resets so the dashboard fills the viewport.
const style = document.createElement('style')
style.textContent = `
    body {
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Roboto", "Oxygen",
            "Ubuntu", "Cantarell", "Fira Sans", "Droid Sans", "Helvetica Neue",
            sans-serif, "Noto Color Emoji";
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
