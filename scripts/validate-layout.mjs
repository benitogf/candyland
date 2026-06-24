// Headless layout validation: builds nothing (run after `npm run build`), spawns
// `vite preview`, and asserts the full-screen run workspace never lets content
// overflow the layout — excess must scroll inside the inner containers (the
// terminal / lists), not grow the dialog or the page. Screenshots are saved to
// /tmp/cl-shots for visual inspection. Self-contained: starts and stops its own
// server. Usage: npm run build && npm run validate:layout
import { spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { chromium } from 'playwright'

const PORT = 4319
const BASE = `http://localhost:${PORT}`
const OUT = '/tmp/cl-shots'
const VIEWPORTS = [{ w: 1440, h: 900 }, { w: 1024, h: 720 }, { w: 768, h: 1024 }, { w: 375, h: 812 }]
const ROUTES = [
    '/', '/new', '/tasks', '/how-it-works',
    '/run/sim/agents', '/run/sim/sessions', '/run/sim/board', '/run/sim/tasks', '/run/sim/overview',
    '/run/csv-export/agents', '/run/csv-export/sessions', '/run/csv-export/overview',
    '/run/stress/agents', '/run/stress/board', '/run/stress/tasks', '/run/stress/sessions',
]

mkdirSync(OUT, { recursive: true })

const server = spawn('npx', ['vite', 'preview', '--port', String(PORT)], { stdio: 'ignore' })
const stop = () => { try { server.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)

// Wait for the server to answer.
const waitReady = async () => {
    for (let i = 0; i < 50; i++) {
        try { const r = await fetch(BASE); if (r.ok) return } catch { /* not up yet */ }
        await new Promise((r) => setTimeout(r, 200))
    }
    throw new Error('preview server did not start')
}

await waitReady()

const browser = await chromium.launch()
const rows = []
let bad = 0
for (const vp of VIEWPORTS) {
    const page = await browser.newPage({ viewport: { width: vp.w, height: vp.h } })
    for (const route of ROUTES) {
        await page.goto(BASE + route, { waitUntil: 'networkidle' }).catch(() => {})
        await page.waitForTimeout(700)
        const m = await page.evaluate(() => {
            const de = document.documentElement
            const paper = document.querySelector('.MuiDialog-paper')
            return {
                pageX: de.scrollWidth - window.innerWidth,
                paperY: paper ? paper.scrollHeight - paper.clientHeight : 0,
                paperX: paper ? paper.scrollWidth - paper.clientWidth : 0,
            }
        })
        const ok = m.pageX <= 2 && m.paperY <= 2 && m.paperX <= 2
        if (!ok) bad++
        rows.push({ vp: `${vp.w}x${vp.h}`, route, ...m, ok })
        await page.screenshot({ path: `${OUT}/${vp.w}-${route.replace(/\//g, '_') || '_root'}.png` }).catch(() => {})
    }
    await page.close()
}
await browser.close()
stop()

for (const r of rows) {
    console.log(`${r.ok ? 'OK  ' : 'FAIL'} ${r.vp} ${r.route.padEnd(26)} pageX=${r.pageX} paperY=${r.paperY} paperX=${r.paperX}`)
}
console.log(`\n${rows.length - bad}/${rows.length} layouts contained. Screenshots in ${OUT}`)
process.exit(bad ? 1 : 0)
