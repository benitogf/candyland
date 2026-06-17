// End-to-end: drives the REAL running binary (Go ooo backend + embedded SPA)
// through the guided flow and asserts live state flows from the conductor into
// the UI over ooo. No mocks. Assumes the server is already running (UI :8080,
// realtime :8888) — start it with CANDYLAND_EXECUTOR=scripted for determinism.
import { spawn } from 'node:child_process'
import { chromium } from 'playwright'

const UI = 'http://localhost:8080'
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }

const srv = spawn(bin, [], { stdio: 'ignore', env: { ...process.env, CANDYLAND_EXECUTOR: 'scripted' } })
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)
for (let i = 0; i < 50; i++) { try { if ((await fetch(UI)).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

const browser = await chromium.launch()
const p = await browser.newPage({ viewport: { width: 1280, height: 900 } })
try {
    await p.goto(UI, { waitUntil: 'networkidle' })
    check('SPA served by the Go binary', await p.getByText('Start something new').isVisible())

    await p.getByRole('button', { name: 'Start a new run' }).click()
    const w = p.getByRole('dialog')
    await w.getByText('Non-developer', { exact: true }).click()
    await w.getByRole('button', { name: 'Next' }).click()
    await w.getByText('Web app', { exact: true }).click()
    await w.getByRole('button', { name: 'Next' }).click()
    await w.getByPlaceholder(/Add a CSV export/).fill('Let people download their reports as a CSV')
    await w.getByRole('button', { name: 'Start run' }).click()

    // The run was created on the backend; the workspace opens on its real id.
    await p.waitForURL(/\/run\/r\d+/, { timeout: 8000 })
    check('run created on the backend (real id in URL)', /\/run\/r\d+/.test(p.url()), p.url().split('/').pop())

    // Planning questions came from the backend (loading → question).
    await p.getByText('Who is this for?').waitFor({ state: 'visible', timeout: 8000 })
    check('planning questions fetched from the backend', true)
    await p.getByText('Everyone', { exact: true }).click()
    await p.getByText('A one-time action').waitFor({ timeout: 6000 })
    await p.getByText('A one-time action').click()
    await p.getByText(/Which of these matter/).waitFor({ timeout: 6000 })
    await p.getByText('Works on mobile').click()
    await p.getByRole('button', { name: /Start building/ }).click()

    // Build begins on the backend; live state streams over ooo. Progress moves.
    const bar = p.locator('[aria-label="run progress"]')
    await bar.waitFor({ state: 'visible', timeout: 8000 })
    await p.waitForTimeout(1500)
    const v1 = Number(await bar.getAttribute('aria-valuenow'))
    await p.waitForTimeout(4000)
    const v2 = Number(await bar.getAttribute('aria-valuenow'))
    check('live progress streams from the backend over ooo', v2 > v1, `valuenow ${v1} → ${v2}`)

    // The dashboard reflects the live run too.
    await p.getByRole('button', { name: 'close' }).click().catch(() => {})
    await p.goto(UI, { waitUntil: 'networkidle' })
    await p.waitForTimeout(800)
    check('dashboard shows the live run', await p.getByText('Let people download their reports as a').first().isVisible().catch(() => false))
} catch (e) {
    check('e2e flow', false, e.message.split('\n')[0])
}
await browser.close()
stop()
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} checks passed.`)
process.exit(bad ? 1 : 0)
