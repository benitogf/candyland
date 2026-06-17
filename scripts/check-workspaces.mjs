// Lightweight node-managed REST check of workspace persistence (no browser).
// Spawns the real binary, verifies seed + create + delete over the API, then
// stops its own child. Usage: npm run build && node scripts/check-workspaces.mjs
import { spawn } from 'node:child_process'

const API = 'http://localhost:8888'
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const srv = spawn(bin, [], { stdio: 'ignore', env: { ...process.env, CANDYLAND_EXECUTOR: 'scripted' } })
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)

const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }
const j = async (p, opts) => { const r = await fetch(API + p, opts); return { status: r.status, body: r.status === 200 ? await r.json() : null } }

for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/workspaces/web')).status === 200) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

try {
    const web = await j('/workspaces/web')
    check('seeded default workspace served', web.status === 200 && web.body?.data?.label === 'Web app', web.body?.data?.label)

    await j('/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Test WS', folders: ['~/x', '~/y'] }) })
    const created = await j('/workspaces/test-ws')
    check('create persists a workspace', created.status === 200 && created.body?.data?.folders?.length === 2, JSON.stringify(created.body?.data?.folders))

    await j('/api/workspaces/test-ws', { method: 'DELETE' })
    const gone = await fetch(API + '/workspaces/test-ws')
    check('delete removes a workspace', gone.status !== 200, `status ${gone.status}`)
} catch (e) {
    check('workspace REST flow', false, e.message.split('\n')[0])
}

stop()
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} workspace checks passed.`)
process.exit(bad ? 1 : 0)
