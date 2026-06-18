// Node-managed REST check of /api/system (platform + dependency detection).
// Spawns the real binary, asserts the endpoint reports a sane platform, deps,
// and executor, then stops its own child. No browser.
import { spawn } from 'node:child_process'

const API = 'http://localhost:8888'
const bin = process.env.CANDYLAND_BIN || '/tmp/candyland'
const srv = spawn(bin, [], { stdio: 'ignore', env: { ...process.env } })
process.on('exit', () => { try { srv.kill('SIGTERM') } catch { /* ignore */ } })

const results = []
const check = (n, ok, d = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${n}${d ? ` — ${d}` : ''}`) }

for (let i = 0; i < 50; i++) { try { if ((await fetch(API + '/api/system')).ok) break } catch { /* wait */ } await new Promise((r) => setTimeout(r, 200)) }

try {
    const sys = await (await fetch(API + '/api/system')).json()
    console.log('  system →', JSON.stringify(sys).slice(0, 240))
    check('platform detected', ['Linux', 'WSL', 'macOS', 'Windows'].includes(sys.platform), `${sys.platform} (${sys.os}/${sys.arch})`)
    check('version reported', typeof sys.version === 'string' && sys.version.length > 0, sys.version)
    check('deps include claude + git', sys.deps?.some((d) => d.name === 'claude') && sys.deps?.some((d) => d.name === 'git'))
    const claude = sys.deps?.find((d) => d.name === 'claude')
    check('claude dep has an install command', !!claude?.install, claude?.install)
    check('executor + simulated coherent', (sys.executor === 'claude') === !sys.simulated, `executor=${sys.executor} simulated=${sys.simulated}`)
    check('recommendations present', Array.isArray(sys.recommendations) && sys.recommendations.length > 0)
} catch (e) {
    check('system endpoint', false, e.message.split('\n')[0])
}

const bad = results.filter((r) => !r).length
console.log(`\n${results.length - bad}/${results.length} system checks passed.`)
process.exit(bad ? 1 : 0)
