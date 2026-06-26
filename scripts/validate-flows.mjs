// Browser smoke against the REAL binary (Go backend + embedded SPA), wired to a
// stub claude + stub gh + a throwaway git repo so it's deterministic and spends
// no Anthropic tokens. candyland is a sidecar now: runs are launched from the
// editor (the candyland MCP) or, secondarily, from the dashboard wizard by
// naming the repo folder. This proves the secondary web flow + the lifecycle:
//   1. the dashboard leads with the editor-launch hint + a secondary "Start one here";
//   2. the wizard takes a repository folder (typed) + a prompt — no workspace concept;
//   3. starting a run shows the planning Q&A (fetched from the backend);
//   4. you can CANCEL during the questions and land back on the dashboard;
//   5. a cleared run stays in the Tasks history;
//   6. a finished run offers Edit, which re-opens planning.
// Runs on non-default ports — the binary injects its API port into the SPA — so
// it never collides with a candyland already running on 8888.
// Usage: npm run build && go build -o /tmp/candyland . && CANDYLAND_BIN=/tmp/candyland node scripts/validate-flows.mjs
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, chmodSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright'

const API_PORT = '28970'
const SPA_PORT = '28971'
const UI = `http://localhost:${SPA_PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
if (!existsSync(bin)) {
    console.error(`candyland binary not found at ${bin}.\nBuild it first:  go build -o ${bin} .   (or set CANDYLAND_BIN)`)
    process.exit(1)
}
const results = []
const check = (name, ok, detail = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ` — ${detail}` : ''}`) }
const git = (dir, ...a) => execFileSync('git', a, { cwd: dir, encoding: 'utf8' })

// Fixtures: throwaway repo + origin, stub claude, stub gh.
const root = mkdtempSync(join(tmpdir(), 'candyland-ui-'))
const repo = join(root, 'repo')
execFileSync('mkdir', ['-p', repo])
git(repo, 'init', '-q', '-b', 'main')
git(repo, 'config', 'user.email', 'ui@candyland.local')
git(repo, 'config', 'user.name', 'candyland ui')
writeFileSync(join(repo, 'README.md'), '# ui\n'); git(repo, 'add', '-A'); git(repo, 'commit', '-q', '-m', 'init')
git(repo, 'init', '--bare', '-q', join(root, 'origin.git')); git(repo, 'remote', 'add', 'origin', join(root, 'origin.git'))
const stubClaude = join(root, 'claude')
// planner → a generated question (proves the Q&A is from a real call); tech lead →
// a one-task partition; coder → a real file write + a TEST line. Lets a run
// complete so the finished-run controls (Edit) and the audit can be exercised.
writeFileSync(stubClaude, `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"clarifying questions"* ]]; then
  echo '{"type":"result","result":"[{\\"id\\":\\"scope\\",\\"question\\":\\"Export all rows or the filtered view?\\",\\"options\\":[\\"All rows\\",\\"Filtered view\\"]}]"}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\\"id\\":\\"a\\",\\"title\\":\\"task a\\",\\"files\\":[\\"a.txt\\"],\\"test\\":\\"a_test\\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":1}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "work $$" > "candyland_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\\"pass\\":1,\\"fail\\":0}"}]}}'
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":2}}'
fi
`)
chmodSync(stubClaude, 0o755)
const stubGh = join(root, 'gh')
writeFileSync(stubGh, "#!/usr/bin/env bash\necho 'https://github.com/example/repo/pull/7'\n"); chmodSync(stubGh, 0o755)

const srv = spawn(bin, ['--port', API_PORT, '--spaPort', SPA_PORT, '--dataPath', join(root, 'data')], {
    stdio: 'ignore',
    env: { ...process.env, HOME: root, CANDYLAND_CLAUDE: stubClaude, CANDYLAND_GH: stubGh },
})
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)
for (let i = 0; i < 50; i++) { try { if ((await fetch(`http://localhost:${API_PORT}/api/system`)).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

const browser = await chromium.launch()
const p = await browser.newPage({ viewport: { width: 1280, height: 900 } })
// An uncaught exception (e.g. a React render crash) blanks the page; collect them
// so a crash FAILS the run instead of only showing up as a downstream timeout.
const pageErrors = []
p.on('pageerror', (e) => { pageErrors.push(e.message); console.log('PAGEERROR:', e.message) })
p.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE.ERR:', m.text().slice(0, 300)) })

// Open the secondary web wizard and fill it (mode → repo folder → prompt step).
const openWizard = async () => {
    await p.getByRole('button', { name: 'Start one here' }).first().click()
    const wiz = p.getByRole('dialog')
    await wiz.getByText('Non-developer', { exact: true }).click()
    await wiz.getByRole('button', { name: 'Next' }).click()
    await wiz.getByRole('heading', { name: 'Which repository?' }).waitFor({ state: 'visible', timeout: 6000 })
    await wiz.getByLabel('Repository folder').fill(repo)
    await wiz.getByRole('button', { name: 'Next' }).click()
    return wiz
}

try {
    await p.goto(UI, { waitUntil: 'networkidle' })

    // The dashboard leads with the editor-launch story (candyland is a sidecar).
    check('dashboard shows the editor-launch hint', await p.getByText(/Launch from your editor/i).first().isVisible())

    await openWizard()
    check('wizard takes a typed repository folder (no workspace concept)', true)

    // Prompt → start the run.
    await p.getByPlaceholder(/Add a CSV export/).fill('Let people download their reports as a CSV')
    await p.getByRole('button', { name: 'Start run' }).click()

    // Planning Q&A appears — a question GENERATED from the prompt (not canned).
    await p.getByText('Export all rows or the filtered view?').waitFor({ state: 'visible', timeout: 8000 })
    check('planning question generated from the prompt', true)

    // Cancel DURING the questions → back to the dashboard.
    await p.getByRole('button', { name: /Cancel run/ }).click()
    await p.getByRole('button', { name: 'Start one here' }).first().waitFor({ state: 'visible', timeout: 8000 })
    check('can cancel during the planning questions', true)

    // The cancelled run is KEPT (history), shown on the dashboard as Cancelled.
    await p.getByText('Cancelled').first().waitFor({ state: 'visible', timeout: 6000 })
    check('cancelled run is kept on the dashboard (not deleted)', true)

    // Clear it → archived → it leaves the dashboard.
    await p.getByRole('button', { name: 'clear run' }).first().click()
    await p.getByText('Cancelled').first().waitFor({ state: 'detached', timeout: 6000 })
    check('clear removes the run from the dashboard', true)

    // …but it's still in the Tasks history, flagged as cleared.
    await p.getByRole('button', { name: 'Tasks' }).click()
    await p.getByText('cleared').first().waitFor({ state: 'visible', timeout: 6000 })
    check('cleared run is still in the Tasks history', await p.getByText('Cancelled').first().isVisible())

    // ── Edit a finished run: distinct from restart, it re-opens planning. ──
    await p.getByRole('button', { name: 'Dashboard' }).click()
    await openWizard()
    await p.getByPlaceholder(/Add a CSV export/).fill('first version of the request')
    await p.getByRole('button', { name: 'Start run' }).click()
    await p.getByText('Export all rows or the filtered view?').waitFor({ state: 'visible', timeout: 8000 })
    await p.getByText('All rows', { exact: true }).click()

    // The run builds to completion; a finished run offers Edit (not just Restart).
    await p.getByRole('button', { name: 'Edit' }).first().waitFor({ state: 'visible', timeout: 30000 })
    check('finished run offers Edit', await p.getByRole('button', { name: 'Edit' }).first().isVisible())

    // Edit the request → save → it returns to planning and re-asks the questions.
    await p.getByRole('button', { name: 'Edit' }).first().click()
    const editDlg = p.getByRole('dialog').last()
    await editDlg.getByPlaceholder(/Describe the change/).fill('a changed request after editing')
    await editDlg.getByRole('button', { name: /Save.*re-plan/ }).click()
    await p.getByText('Export all rows or the filtered view?').waitFor({ state: 'visible', timeout: 10000 })
    check('editing a finished run re-opens planning (questions regenerate)', await p.getByText('Export all rows or the filtered view?').first().isVisible())
} catch (e) {
    check('ui smoke', false, e.message.split('\n')[0])
}
check('no uncaught page errors (UI never crashed)', pageErrors.length === 0, pageErrors[0] || '')
await browser.close()
stop()
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} UI checks passed.`)
process.exit(bad ? 1 : 0)
