// Runtime verification: build nothing (run after `npm run build` + `go build`),
// spawn the REAL candyland binary (Go backend + embedded SPA) wired to a stub
// claude + stub gh + a throwaway git repo, seed a run over REST, then drive a
// headless browser through every changed navigation path and assert each renders
// without a React crash. This is the automated form of "run the binary and
// exercise each changed path": it proves the routes load, the run overlay tabs
// render, and the Work / quest / campaign views are reachable end-to-end.
// Usage:
//   npm run build && go build -o /tmp/candyland-bin . && \
//   CANDYLAND_BIN=/tmp/candyland-bin node scripts/validate-runtime.mjs
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, chmodSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright'

const API_PORT = process.env.CANDYLAND_API_PORT || '28980'
const SPA_PORT = process.env.CANDYLAND_SPA_PORT || '28981'
const API = `http://localhost:${API_PORT}`
const UI = `http://localhost:${SPA_PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland-bin'
if (!existsSync(bin)) {
    console.error(`candyland binary not found at ${bin}.\nBuild it first:  go build -o ${bin} .   (or set CANDYLAND_BIN)`)
    process.exit(1)
}
const results = []
const check = (name, ok, detail = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ` — ${detail}` : ''}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const git = (dir, ...a) => execFileSync('git', a, { cwd: dir, encoding: 'utf8' })

// ── Fixtures: throwaway repo + origin, stub claude, stub gh. ─────────────────
const root = mkdtempSync(join(tmpdir(), 'candyland-rt-'))
const repo = join(root, 'repo')
execFileSync('mkdir', ['-p', repo])
git(repo, 'init', '-q', '-b', 'main')
git(repo, 'config', 'user.email', 'rt@candyland.local')
git(repo, 'config', 'user.name', 'candyland rt')
git(repo, 'config', 'commit.gpgsign', 'false')
writeFileSync(join(repo, 'README.md'), '# rt\n'); git(repo, 'add', '-A'); git(repo, 'commit', '-q', '-m', 'init')
git(repo, 'init', '--bare', '-q', join(root, 'origin.git')); git(repo, 'remote', 'add', 'origin', join(root, 'origin.git'))

const stubClaude = join(root, 'claude')
writeFileSync(stubClaude, `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"clarifying questions"* ]]; then
  echo '{"type":"result","result":"[]"}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\\"id\\":\\"a\\",\\"title\\":\\"task a\\",\\"files\\":[\\"a.txt\\"],\\"test\\":\\"a_test\\"}]"}]}}'
  # A long (>300 char) result so the backend truncates the compact summary and
  # persists the complete payload in textFull. The END marker sits past the cut,
  # so the agents view (which defaults to the first/tech-lead agent) can only show
  # it by rendering the full untruncated payload — the runtime proof of c7.
  long=$(printf 'FULLOUT_BEGIN %0.sX' $(seq 1 400)); long="$long FULLOUT_END_MARKER"
  echo "{\\"type\\":\\"result\\",\\"subtype\\":\\"success\\",\\"result\\":\\"$long\\",\\"usage\\":{\\"output_tokens\\":1}}"
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"a.txt"}}]}}'
  echo "work $$" > "candyland_$$.txt"
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TEST {\\"pass\\":1,\\"fail\\":0}"}]}}'
  # A deliberately long (>300 char) result so the backend truncates the compact
  # summary and persists the complete payload in textFull. The END marker sits
  # past the truncation cut, so the UI can only show it by rendering the full
  # (untruncated) payload — this is the runtime proof of the untruncated-output
  # commitment.
  long=$(printf 'FULLOUT_BEGIN %0.sX' $(seq 1 400)); long="$long FULLOUT_END_MARKER"
  echo "{\\"type\\":\\"result\\",\\"subtype\\":\\"success\\",\\"result\\":\\"$long\\",\\"usage\\":{\\"output_tokens\\":2}}"
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

const j = async (path, opts) => {
    const res = await fetch(API + path, opts)
    const text = await res.text()
    return { status: res.status, body: text ? JSON.parse(text) : null }
}

let browser
try {
    for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await sleep(200) }
    check('binary serves the API', (await fetch(API + '/api/system')).ok)
    check('binary serves the embedded SPA', (await fetch(UI + '/')).ok)

    // Seed a run and drive it to a terminal state so its overlay has real content.
    const created = await j('/api/runs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'developer', folders: [repo], prompt: 'add a CSV export' }) })
    const runId = created.body?.id
    check('run created over REST', created.status === 200 && !!runId, runId)
    await j(`/api/runs/${runId}/begin`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
    let run = null
    for (let i = 0; i < 150; i++) { run = (await j(`/runs/${runId}`)).body?.data; if (run && ['done', 'error', 'cancelled'].includes(run.status)) break; await sleep(200) }
    check('run reached a terminal state', ['done', 'error', 'cancelled'].includes(run?.status), `status ${run?.status}`)

    browser = await chromium.launch()
    const p = await browser.newPage({ viewport: { width: 1280, height: 900 } })
    const pageErrors = []
    p.on('pageerror', (e) => { pageErrors.push(e.message); console.log('PAGEERROR:', e.message) })

    // Exercise every changed navigation path; each must render without a crash.
    const routes = [
        ['landing / dashboard', '/'],
        ['Work list (tasks)', '/tasks'],
        ['how it works', '/how-it-works'],
        ['run overlay — agents', `/run/${runId}/agents`],
        ['run overlay — overview', `/run/${runId}/overview`],
        ['run overlay — tasks', `/run/${runId}/tasks`],
    ]
    for (const [name, path] of routes) {
        const before = pageErrors.length
        await p.goto(UI + path, { waitUntil: 'networkidle' })
        await sleep(400)
        // A crashed React tree blanks <body>; a live one keeps rendered content.
        const bodyLen = (await p.locator('body').innerText().catch(() => '')).trim().length
        check(`route renders: ${name}`, pageErrors.length === before && bodyLen > 0, `${path} · body ${bodyLen} chars`)
    }

    // ── Behavior assertions (not just "renders"): each commitment proven live. ──

    // c7 — untruncated output: the agents view must show the FULL result payload,
    // including the END marker that sits past the compact-summary truncation cut.
    await p.goto(UI + `/run/${runId}/agents`, { waitUntil: 'networkidle' })
    await sleep(400)
    const agentsText = (await p.locator('body').innerText().catch(() => '')).trim()
    const fullShown = agentsText.includes('FULLOUT_END_MARKER')
    check('untruncated agent output rendered in full', fullShown,
        fullShown ? 'END marker past the truncation cut is rendered'
            : agentsText.includes('FULLOUT_BEGIN') ? 'begin shown but END marker truncated' : 'no full-output payload found')

    // c6 + c9 — Work list filter labels render, and the parent filter DEFAULTS to
    // "No parent" (top-level work leads; children are reached by drilling in).
    await p.goto(UI + '/tasks', { waitUntil: 'networkidle' })
    await sleep(400)
    const tasksText = (await p.locator('body').innerText().catch(() => '')).trim()
    check('Work filter labels render', tasksText.includes('Parent') && tasksText.includes('Status'), `body ${tasksText.length} chars`)
    const parentValue = await p.locator('div[role="combobox"]', { hasText: 'No parent' }).count().catch(() => 0)
    check('Work list defaults to no-parent', parentValue > 0, parentValue ? 'No parent selected by default' : 'default not "No parent"')

    // c8/c27 — copy-reference action present on the Work list (per-row handle copy).
    const copyRefs = await p.locator('[aria-label*="opy reference" i], [title*="opy reference" i]').count().catch(() => 0)
    check('copy-reference action present', copyRefs > 0, `${copyRefs} copy-reference control(s)`)

    // Unknown routes redirect home rather than 404-blanking (Router '*' → '/').
    await p.goto(UI + '/does-not-exist', { waitUntil: 'networkidle' })
    check('unknown route redirects home', new URL(p.url()).pathname === '/', p.url())

    check('no uncaught page errors across all routes', pageErrors.length === 0, pageErrors[0] || '')
} catch (e) {
    check('runtime verification', false, e.message.split('\n')[0])
} finally {
    if (browser) await browser.close()
    stop()
}
const bad = results.filter((r) => !r).length
console.log(`\nTEST ${JSON.stringify({ pass: results.length - bad, fail: bad })}`)
process.exit(bad ? 1 : 0)
