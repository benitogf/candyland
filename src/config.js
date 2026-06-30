// Where the orchestrator backend lives. The dashboard talks to it over REST and
// the ooo WebSocket client. The API port is injected into index.html by the Go
// binary (window.__CANDYLAND_API_PORT__) so it tracks the binary's --port flag
// instead of being hardcoded; it falls back to 8888 (the default, and the dev
// `npm run dev` case where nothing injects it).
const isFileProtocol = window.location.protocol === 'file:'
const apiPort = (typeof window !== 'undefined' && window.__CANDYLAND_API_PORT__) || 8888
const host = isFileProtocol ? 'localhost' : window.location.hostname

export const domain = `${host}:${apiPort}`
export const ssl = false

// Brand palette — kept in one place so the MUI theme and Mermaid diagrams agree.
// Near-black canvas with neon candy highlights (hot pink, lime, cyan) pulled
// from the toy/eyeball-candy reference. Cyan is the dominant accent (primary).
export const candy = {
    pink: '#ff2e88',   // hot magenta — supporting accent
    sky: '#2fd6e6',    // cyan/teal — dominant accent (primary)
    mint: '#9be23a',   // neon lime (the little creature)
    grape: '#a06bff',  // purple (the eyeballs) — supporting accent
    lemon: '#ffd93d',  // warm highlight
    bgDark: '#08080a',   // near-black dominant canvas
    bgPaper: '#131316',  // panels
    bgPaperHi: '#1d1d22', // hovered panels
    bgInk: '#050506',    // deepest wells (code / live output)
    line: '#2a2a31',     // borders
}
