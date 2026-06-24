// Lightweight node-managed REST check of workspace persistence (no browser).
// Spawns the real binary on a throwaway data path, verifies a fresh install
// starts EMPTY, then create + read-back + delete over the API (the exact flow
// that was reported broken). Usage: npm run build && node scripts/check-workspaces.mjs
import { spawn } from 'node:child_process'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = process.env.CANDYLAND_PORT || '8888'
const SPA_PORT = process.env.CANDYLAND_SPA_PORT || '8080'
const API = `http://localhost:${PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const workRoot = mkdtempSync(join(tmpdir(), 'candyland-ws-'))
const dataPath = join(workRoot, 'data')
const realFolder = join(workRoot, 'repo') // a real, readable+writable dir to point a workspace at
mkdirSync(realFolder, { recursive: true })
const srv = spawn(bin, ['--port', PORT, '--spaPort', SPA_PORT, '--dataPath', dataPath], { stdio: 'ignore', env: { ...process.env } })
const stop = () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } }
process.on('exit', stop)

const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }
const j = async (p, opts) => { const r = await fetch(API + p, opts); return { status: r.status, body: r.status === 200 ? await r.json() : null } }

for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

try {
    // A fresh install ships no demo data — the glob is empty until the user creates one.
    const empty = await j('/workspaces/*')
    check('fresh install starts with no workspaces', empty.status === 200 && Array.isArray(empty.body) && empty.body.length === 0, `count ${empty.body?.length}`)

    // A non-existent folder is rejected (must be a real, readable+writable dir).
    const badRes = await fetch(API + '/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Bad WS', folders: [join(workRoot, 'does-not-exist')] }) })
    check('rejects a folder that is not on disk', badRes.status === 400, `status ${badRes.status}`)

    // A multi-word name (spaces) is the case that used to silently fail: the
    // server derives an alphanumeric, ooo-key-safe id ("Test WS" → "testws").
    const postRes = await j('/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Test WS', folders: [realFolder] }) })
    check('create returns the key-safe id', postRes.status === 200 && postRes.body?.id === 'testws', `id ${postRes.body?.id}`)
    // The store settles asynchronously (the real client gets it over ws); poll the read.
    let created = { status: 0 }
    for (let i = 0; i < 20; i++) {
        created = await j('/workspaces/testws')
        if (created.status === 200) break
        await new Promise((r) => setTimeout(r, 100))
    }
    check('create persists a workspace with the real folder', postRes.status === 200 && created.status === 200 && created.body?.data?.folders?.[0] === realFolder, `post ${postRes.status}, read ${created.status}, folder ${created.body?.data?.folders?.[0]}`)

    // Soft-delete: the DELETE marks the record deleted but KEEPS it (and its
    // folders on disk) so the Tasks history can still show the name. The record
    // must remain readable with deleted=true (a hard delete would orphan history).
    const delRes = await fetch(API + '/api/workspaces/testws', { method: 'DELETE' })
    check('delete returns 204 (no active runs)', delRes.status === 204, `status ${delRes.status}`)
    let soft = { status: 0 }
    for (let i = 0; i < 20; i++) {
        soft = await j('/workspaces/testws')
        if (soft.status === 200 && soft.body?.data?.deleted === true) break
        await new Promise((r) => setTimeout(r, 100))
    }
    check('soft-delete keeps the record with deleted=true', soft.status === 200 && soft.body?.data?.deleted === true, `deleted ${soft.body?.data?.deleted}`)

    // Recreating the same slug reattaches: the soft-delete is cleared (deleted=false).
    const reRes = await j('/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Test WS', folders: [realFolder] }) })
    let revived = { status: 0 }
    for (let i = 0; i < 20; i++) {
        revived = await j('/workspaces/testws')
        if (revived.status === 200 && !revived.body?.data?.deleted) break
        await new Promise((r) => setTimeout(r, 100))
    }
    check('recreating the slug clears the soft-delete', reRes.status === 200 && revived.body?.data?.deleted !== true, `deleted ${revived.body?.data?.deleted}`)

    // A workspace referenced by an ACTIVE run can't be deleted: the API returns 409
    // with the blocking runs, so the UI can offer to cancel them first.
    const runRes = await j('/api/runs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: 'developer', workspace: 'testws', prompt: 'a task that holds the workspace' }) })
    const blockedRes = await fetch(API + '/api/workspaces/testws', { method: 'DELETE' })
    let blockingBody = null
    if (blockedRes.status === 409) blockingBody = await blockedRes.json().catch(() => null)
    check('delete is blocked (409) while an active run references it', blockedRes.status === 409, `status ${blockedRes.status}`)
    check('the 409 lists the blocking run', Array.isArray(blockingBody?.blocking) && blockingBody.blocking.some((b) => b.id === runRes.body?.id), `blocking ${JSON.stringify(blockingBody?.blocking)}`)

    // Cancel the blocking run, then the delete goes through (the cancel-then-delete flow).
    await fetch(API + `/api/runs/${runRes.body?.id}/cancel`, { method: 'POST' })
    let afterCancel = { status: 0 }
    for (let i = 0; i < 30; i++) {
        afterCancel = await fetch(API + '/api/workspaces/testws', { method: 'DELETE' })
        if (afterCancel.status === 204) break
        await new Promise((r) => setTimeout(r, 100))
    }
    check('after cancelling the run, the delete succeeds', afterCancel.status === 204, `status ${afterCancel.status}`)
} catch (e) {
    check('workspace REST flow', false, e.message.split('\n')[0])
}

stop()
const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} workspace checks passed.`)
process.exit(bad ? 1 : 0)
