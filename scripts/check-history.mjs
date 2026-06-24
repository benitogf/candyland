// REST check of the Tasks history semantics (no browser): a cancelled run is
// KEPT as "cancelled" (not deleted), and "clear" archives it (hidden from the
// dashboard, still present in the history). Drives the real binary on a throwaway
// data path. Usage: npm run build && CANDYLAND_BIN=/tmp/candyland node scripts/check-history.mjs
import { spawn } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = process.env.CANDYLAND_PORT || '8888'
const SPA_PORT = process.env.CANDYLAND_SPA_PORT || '8080'
const API = `http://localhost:${PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const dataPath = join(mkdtempSync(join(tmpdir(), 'candyland-hist-')), 'data')
const srv = spawn(bin, ['--port', PORT, '--spaPort', SPA_PORT, '--dataPath', dataPath], { stdio: 'ignore', env: { ...process.env } })
process.on('exit', () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } })

const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }
const j = async (p, opts) => { const r = await fetch(API + p, opts); return { status: r.status, body: r.status === 200 ? await r.json() : null } }
const post = (p) => fetch(API + p, { method: 'POST' })
const run = async (id) => (await j(`/runs/${id}`)).body?.data

for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

try {
    const created = await j('/api/runs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'developer', workspace: 'ws', prompt: 'history test' }) })
    const id = created.body?.id
    check('run created (planning)', (await run(id))?.status === 'planning', id)

    // Cancel during planning → KEPT as "cancelled" (not 404 / removed).
    await post(`/api/runs/${id}/cancel`)
    let r = {}
    for (let i = 0; i < 20; i++) { r = await run(id); if (r?.status === 'cancelled') break; await new Promise((res) => setTimeout(res, 100)) }
    check('cancel keeps the run as "cancelled" (history)', r?.status === 'cancelled', `status ${r?.status}`)
    check('cancelled run is not archived yet', r?.archived !== true)

    // Clear → archived, but still present in the run history.
    await post(`/api/runs/${id}/archive`)
    for (let i = 0; i < 20; i++) { r = await run(id); if (r?.archived) break; await new Promise((res) => setTimeout(res, 100)) }
    check('clear archives the run (kept in history)', r?.archived === true && r?.status === 'cancelled', `archived ${r?.archived} status ${r?.status}`)

    const all = await j('/runs/*')
    check('archived run still in the runs history', Array.isArray(all.body) && all.body.some((e) => e?.data?.id === id), `count ${all.body?.length}`)
} catch (e) {
    check('history flow', false, e.message.split('\n')[0])
}

try { srv.kill('SIGTERM') } catch { /* ignore */ }
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} history checks passed.`)
process.exit(bad ? 1 : 0)
