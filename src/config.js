// Where the orchestrator backend lives. The dashboard talks to it over the
// ooo WebSocket client once the live views (Board/Agents/Tasks) are wired.
const isFileProtocol = window.location.protocol === 'file:'

export const domain = isFileProtocol ? 'localhost:8888' : window.location.hostname + ':8888'
export const ssl = false

// Brand palette — kept in one place so the MUI theme and Mermaid diagrams agree.
// Near-black canvas with neon candy highlights (hot pink, lime, cyan) pulled
// from the toy/eyeball-candy reference. Mode swaps the dominant accent: cyan
// for developer (cool, technical), hot pink for non-developer (warm, friendly).
export const candy = {
    pink: '#ff2e88',   // hot magenta — non-developer accent
    sky: '#2fd6e6',    // cyan/teal — developer accent
    mint: '#9be23a',   // neon lime (the little creature)
    grape: '#a06bff',  // purple (the eyeballs) — supporting accent
    lemon: '#ffd93d',  // warm highlight
    bgDark: '#08080a',   // near-black dominant canvas
    bgPaper: '#131316',  // panels
    bgPaperHi: '#1d1d22', // hovered panels
    bgInk: '#050506',    // deepest wells (code / live output)
    line: '#2a2a31',     // borders
}

// The dominant accent per mode — two shades of the palette that swap with mode.
export const modeAccent = (mode) => (mode === 'developer' ? candy.sky : candy.pink)
