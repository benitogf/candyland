// Headless validation of every Mermaid diagram embedded in the dashboard pages.
// The Vite build does NOT catch Mermaid syntax errors (they render client-side),
// so this parses each diagram with Mermaid's own parser to fail fast.
//
// Usage: node scripts/validate-diagrams.mjs
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { JSDOM } from 'jsdom'

// Mermaid expects a browser environment.
const dom = new JSDOM('<!DOCTYPE html><body></body>', { pretendToBeVisual: true })
globalThis.window = dom.window
globalThis.document = dom.window.document

const { default: mermaid } = await import('mermaid')
mermaid.initialize({ startOnLoad: false, securityLevel: 'loose' })

// Pull every `const NAME = \`...\`` template literal out of a source file.
function extractTemplates(src) {
    const out = []
    const re = /const\s+([A-Z0-9_]+)\s*=\s*`([\s\S]*?)`/g
    let m
    while ((m = re.exec(src)) !== null) {
        out.push({ name: m[1], body: m[2].trim() })
    }
    return out
}

// A template is a Mermaid diagram if it opens with a known diagram keyword.
const DIAGRAM_KEYWORDS = [
    'flowchart', 'graph', 'sequenceDiagram', 'stateDiagram', 'stateDiagram-v2',
    'gantt', 'xychart-beta', 'pie', 'classDiagram', 'erDiagram', 'journey', 'mindmap', 'timeline',
]
const looksLikeDiagram = (body) => DIAGRAM_KEYWORDS.some((k) => body.startsWith(k))

// Walk src/ for every .js file (diagrams live in pages/ and panels/).
function jsFiles(dir) {
    const out = []
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name)
        if (entry.isDirectory()) out.push(...jsFiles(full))
        else if (entry.name.endsWith('.js')) out.push(full)
    }
    return out
}

const files = jsFiles('src')

let failures = 0
let checked = 0
for (const file of files) {
    const src = readFileSync(file, 'utf8')
    const label = relative('src', file)
    for (const { name, body } of extractTemplates(src)) {
        if (!looksLikeDiagram(body)) continue
        checked++
        try {
            await mermaid.parse(body)
            console.log(`  ok   ${label} · ${name}`)
        } catch (e) {
            failures++
            console.error(`  FAIL ${label} · ${name}\n       ${String(e.message || e).split('\n').join('\n       ')}`)
        }
    }
}

console.log(`\n${checked - failures}/${checked} diagrams parsed cleanly.`)
process.exit(failures ? 1 : 0)
