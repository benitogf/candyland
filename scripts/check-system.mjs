// Node-managed REST check of /api/system (platform + dependency detection).
// Spawns the real binary, asserts the endpoint reports a sane platform and the
// real dependencies (claude/git/gh), then stops its own child. No browser.
import { spawn } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PORT = process.env.CANDYLAND_PORT || '8888'
const SPA_PORT = process.env.CANDYLAND_SPA_PORT || '8080'
const API = `http://localhost:${PORT}`
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
// Isolate storage in a throwaway dir — sharing the default ./db/data with a
// candyland you already ran can deadlock the embedded store on startup.
const dataPath = join(mkdtempSync(join(tmpdir(), 'candyland-sys-')), 'data')
const srv = spawn(bin, ['--port', PORT, '--spaPort', SPA_PORT, '--dataPath', dataPath], { stdio: 'ignore', env: { ...process.env } })
process.on('exit', () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } })

const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }

for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

try {
    const sys = await (await fetch(API + '/api/system')).json()
    console.log('  system →', JSON.stringify(sys).slice(0, 240))
    check('platform detected', ['Linux', 'WSL', 'macOS', 'Windows'].includes(sys.platform), `${sys.platform} (${sys.os}/${sys.arch})`)
    check('version reported', typeof sys.version === 'string' && sys.version.length > 0, sys.version)
    const names = (sys.deps || []).map((d) => d.name)
    check('deps include claude + git + gh', ['claude', 'git', 'gh'].every((n) => names.includes(n)), names.join(', '))
    check('every dep has an install command', (sys.deps || []).every((d) => !!d.install))
    check('recommendations present', Array.isArray(sys.recommendations) && sys.recommendations.length > 0)
} catch (e) {
    check('system endpoint', false, e.message.split('\n')[0])
}

const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} system checks passed.`)
process.exit(bad ? 1 : 0)
