// Behavioral validation: drives the real UI through the guided wizard and
// MEASURES the dynamic features — slash-command autocomplete (in the wizard
// prompt AND in developer questions), the planning loading state, a progress
// bar that actually moves, the optional auto-title, the expandable terminal, and
// the quick task switcher. Self-contained (spawns preview).
// Usage: npm run build && npm run validate:flows
import { spawn } from 'node:child_process'
import { chromium } from 'playwright'

const PORT = 4323
const BASE = `http://localhost:${PORT}`
const srv = spawn('npx', ['vite', 'preview', '--port', String(PORT)], { stdio: 'ignore' })
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)
for (let i = 0; i < 50; i++) { try { if ((await fetch(BASE)).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

const results = []
const check = (name, ok, detail = '') => { results.push({ name, ok, detail }); console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ` — ${detail}` : ''}`) }

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })

// Walk the wizard to the prompt step. mode = 'Developer' | 'Non-developer'.
// Selectors are scoped to the active dialog so dashboard cards behind it (which
// also show mode labels) don't cause strict-mode ambiguity.
const toPromptStep = async (p, mode, workspace) => {
    await p.goto(BASE + '/', { waitUntil: 'networkidle' })
    await p.getByRole('button', { name: 'Start a new run' }).click()
    const w = p.getByRole('dialog')
    await w.getByText(mode, { exact: true }).click()
    await w.getByRole('button', { name: 'Next' }).click()
    await w.getByText(workspace, { exact: true }).click()
    await w.getByRole('button', { name: 'Next' }).click()
    await w.getByPlaceholder(/Add a CSV export/).waitFor({ state: 'visible', timeout: 4000 })
}

// ── 1) Non-developer: wizard → autocomplete → loading → multiple-choice → progress MOVES
try {
    const p = await ctx.newPage()
    await toPromptStep(p, 'Non-developer', 'Web app')
    const w = p.getByRole('dialog')
    const prompt = w.getByPlaceholder(/Add a CSV export/)
    await prompt.click()
    await prompt.pressSequentially('/pl', { delay: 40 })
    await p.getByText('/plan', { exact: true }).waitFor({ state: 'visible', timeout: 4000 })
    check('autocomplete in the wizard prompt (/pl → /plan)', true)
    await prompt.fill('Let people download their reports as a CSV file')
    await w.getByRole('button', { name: 'Start run' }).click()

    await p.getByText(/Preparing the first question/).waitFor({ state: 'visible', timeout: 3000 })
    check('planning loading state shows before the question', true)
    await p.getByText('Who is this for?').waitFor({ state: 'visible', timeout: 4000 })
    check('multiple-choice question renders', true)
    await p.getByText('Everyone', { exact: true }).click()
    await p.getByText('A one-time action').waitFor({ timeout: 4000 })
    await p.getByText('A one-time action').click()
    await p.getByText(/Which of these matter/).waitFor({ timeout: 4000 })
    await p.getByText('Works on mobile').click()
    await p.getByRole('button', { name: /Start building/ }).click()

    const bar = p.locator('[aria-label="run progress"]')
    await bar.waitFor({ state: 'visible', timeout: 4000 })
    await p.waitForTimeout(1200)
    const v1 = Number(await bar.getAttribute('aria-valuenow'))
    await p.waitForTimeout(3500)
    const v2 = Number(await bar.getAttribute('aria-valuenow'))
    check('progress bar moves during the build', Number.isFinite(v1) && Number.isFinite(v2) && v2 > v1, `valuenow ${v1} → ${v2}`)

    // optional auto-title: no title given → header shows a label derived from the prompt
    const heading = await p.getByRole('dialog').locator('h5').first().innerText()
    check('optional title auto-generated from prompt', /Let people download/i.test(heading), `header "${heading.slice(0, 40)}"`)
    await p.close()
} catch (e) { check('non-developer wizard + progress', false, e.message.split('\n')[0]) }

// ── 2) Developer: question autocomplete → build → Sessions terminal expands → quick switch
try {
    const p = await ctx.newPage()
    await toPromptStep(p, 'Developer', 'Reports API')
    const w = p.getByRole('dialog')
    await w.getByPlaceholder(/Add a CSV export/).fill('add a CSV export endpoint to the reports service')
    await w.getByRole('button', { name: 'Start run' }).click()

    await p.getByText(/What does "done" look like/).waitFor({ state: 'visible', timeout: 4000 })
    const tb = p.getByRole('textbox').first()
    await tb.click()
    await tb.pressSequentially('/g', { delay: 40 })
    await p.getByText('/gh', { exact: true }).waitFor({ state: 'visible', timeout: 4000 })
    check('autocomplete inside developer questions (/g → /gh)', true)
    await tb.fill('')

    for (let i = 0; i < 3; i++) {
        await p.getByRole('textbox').first().fill('acceptance: endpoint returns csv; tests cover it')
        const last = await p.getByRole('button', { name: /Looks good/ }).count()
        if (last) { await p.getByRole('button', { name: /Looks good/ }).click(); break }
        await p.getByRole('button', { name: 'Continue' }).click()
        await p.waitForTimeout(1300)
    }

    await p.getByRole('tab', { name: 'Sessions' }).click()
    await p.getByRole('button', { name: 'expand terminal' }).click()
    await p.waitForTimeout(500)
    check('terminal expand opens a fullscreen terminal', (await p.locator('.xterm').count()) >= 1, `${await p.locator('.xterm').count()} xterm(s)`)
    await p.keyboard.press('Escape') // close expand

    // quick task switcher: open it and jump to another running task without the dashboard
    await p.getByRole('button', { name: 'switch task' }).click()
    await p.getByText('active tasks').waitFor({ state: 'visible', timeout: 3000 })
    const switchTarget = p.getByRole('menuitem').filter({ hasText: 'Add CSV export' }).first()
    await switchTarget.click()
    await p.waitForURL(/\/run\/csv-export/, { timeout: 4000 })
    check('quick task switcher jumps between active runs', true)
    await p.close()
} catch (e) { check('developer wizard + expand + switcher', false, e.message.split('\n')[0]) }

await browser.close()
stop()

const bad = results.filter((r) => !r.ok).length
console.log(`\n${results.length - bad}/${results.length} behaviors verified.`)
process.exit(bad ? 1 : 0)
