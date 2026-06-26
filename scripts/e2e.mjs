// End-to-end: drives the REAL binary (Go ooo backend + conductor) through the
// whole delivery — create a run in a repo folder (the shape the launch_run MCP
// sends), begin the build, and let the conductor partition → code in parallel
// git worktrees → integrate → push → open a PR. No browser, no mocks, and NO
// Anthropic tokens: a stub `claude` (emits a partition + writes real files) and a
// stub `gh` (prints a PR URL) stand in for the only two external commands,
// against a throwaway git repo with a local `origin`. This proves the real
// machinery. The live Claude model behavior is the only thing not exercised
// here — swap CANDYLAND_CLAUDE for the real binary for that.
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, chmodSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = process.env.CANDYLAND_PORT || '28950'
const API = `http://localhost:${PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const git = (dir, ...args) => execFileSync('git', args, { cwd: dir, encoding: 'utf8' })

// ── Fixtures: a throwaway repo with a local origin, a stub claude, a stub gh. ──
const root = mkdtempSync(join(tmpdir(), 'candyland-e2e-'))
const repo = join(root, 'repo')
const bare = join(root, 'origin.git')
execFileSync('mkdir', ['-p', repo])
git(repo, 'init', '-q', '-b', 'main')
git(repo, 'config', 'user.email', 'e2e@candyland.local')
git(repo, 'config', 'user.name', 'candyland e2e')
git(repo, 'config', 'commit.gpgsign', 'false')
writeFileSync(join(repo, 'README.md'), '# e2e\n')
git(repo, 'add', '-A'); git(repo, 'commit', '-q', '-m', 'init')
git(repo, 'init', '--bare', '-q', bare)
git(repo, 'remote', 'add', 'origin', bare)

const stubClaude = join(root, 'claude')
writeFileSync(stubClaude, `#!/usr/bin/env bash
prompt="$2"
if [[ "$prompt" == *"clarifying questions"* ]]; then
  echo '{"type":"result","result":"[{\\"id\\":\\"scope\\",\\"question\\":\\"Which rows?\\",\\"options\\":[\\"All\\",\\"Filtered\\"]}]"}'
elif [[ "$prompt" == *"tech lead"* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"PARTITION [{\\"id\\":\\"a\\",\\"title\\":\\"task a\\",\\"role\\":\\"Backend\\",\\"emoji\\":\\"X\\",\\"files\\":[\\"a.txt\\"],\\"test\\":\\"a_test\\"},{\\"id\\":\\"b\\",\\"title\\":\\"task b\\",\\"role\\":\\"Frontend\\",\\"emoji\\":\\"Y\\",\\"files\\":[\\"b.txt\\"],\\"test\\":\\"b_test\\"}]"}]}}'
  echo '{"type":"result","subtype":"success","result":"ok","usage":{"output_tokens":10}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file":"f"}}]}}'
  echo "work by $$" > "candyland_$$.txt"
  echo '{"type":"result","subtype":"success","result":"green","usage":{"output_tokens":20}}'
fi
`)
chmodSync(stubClaude, 0o755)

const stubGh = join(root, 'gh')
writeFileSync(stubGh, "#!/usr/bin/env bash\necho 'https://github.com/example/repo/pull/7'\n")
chmodSync(stubGh, 0o755)

// ── Spawn the real binary wired to the stubs + a throwaway data path. ──
const srv = spawn(bin, ['--port', PORT, '--spaPort', String(Number(PORT) + 1), '--dataPath', join(root, 'data')], {
    stdio: 'ignore',
    env: { ...process.env, CANDYLAND_CLAUDE: stubClaude, CANDYLAND_GH: stubGh },
})
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)

const j = async (path, opts) => {
    const res = await fetch(API + path, opts)
    const text = await res.text()
    return { status: res.status, body: text ? JSON.parse(text) : null }
}

try {
    for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await sleep(200) }

    // Create a run pointing at the real repo (folders[0] = the git repo it
    // branches/PRs in — the same shape the launch_run MCP sends) and begin the build.
    const created = await j('/api/runs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'developer', folders: [repo], prompt: 'add a CSV export' }) })
    const runId = created.body?.id
    check('run created', created.status === 200 && !!runId, runId)
    await j(`/api/runs/${runId}/begin`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })

    // Let the conductor drive partition → coders → integrate → push → PR.
    let run = null
    for (let i = 0; i < 150; i++) {
        const r = await j(`/runs/${runId}`)
        run = r.body?.data
        if (run && run.status === 'done') break
        await sleep(200)
    }
    check('run reached done', run?.status === 'done', `status ${run?.status} error ${run?.error || ''}`)
    check('run did not error', !run?.error, run?.error || '')
    check('tech lead partitioned 2 tasks', run?.tasks?.length === 2, `tasks ${run?.tasks?.length}`)
    check('both coders green', run?.tasksGreen === 2, `green ${run?.tasksGreen}`)
    check('a real PR url was set', typeof run?.prUrl === 'string' && run.prUrl.includes('/pull/'), run?.prUrl)

    // The real git flow pushed the run branch to origin.
    const refs = git(repo, 'ls-remote', '--heads', 'origin', run?.branch || '')
    check('run branch pushed to origin', refs.includes('refs/heads/' + run?.branch), `branch ${run?.branch}`)

    // Edit the finished run in place: change the request → it resets to planning
    // and the questions regenerate from the new prompt (distinct from restart).
    const edit = await fetch(API + `/api/runs/${runId}/edit`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'developer', folders: [repo], prompt: 'a completely different request', title: 'edited' }) })
    check('edit accepted', edit.status === 204, `status ${edit.status}`)
    let edited = null
    for (let i = 0; i < 20; i++) { edited = (await j(`/runs/${runId}`)).body?.data; if (edited?.status === 'planning') break; await sleep(100) }
    check('edited run reset to planning with the new task', edited?.status === 'planning' && edited?.prompt === 'a completely different request' && !edited?.error, `status ${edited?.status} prompt ${JSON.stringify(edited?.prompt)}`)
    // Questions regenerate from the new prompt (the planner stub returns one).
    const q = await j(`/api/runs/${runId}/questions`)
    check('questions regenerate after edit', Array.isArray(q.body) && q.body.length === 1 && q.body[0].id === 'scope', `questions ${JSON.stringify(q.body).slice(0, 80)}`)
} catch (e) {
    check('e2e delivery', false, e.message.split('\n')[0])
}

stop()
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} e2e checks passed.`)
process.exit(bad ? 1 : 0)
