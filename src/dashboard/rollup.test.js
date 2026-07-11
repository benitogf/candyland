// Regression test for the terminal-status rollup: a run that the backend
// terminates as `blocked` or `delivery-failed` must count as FINISHED, exactly
// like `done` — otherwise the dashboard treats it as still-running.
//
// rollup.js imports react/mui at module scope, so (like AgentsPanel.test.js) we
// bundle it through esbuild and load the bundle as ESM, keeping react/mui
// external so Node resolves them against the project's node_modules.
// Run with:  node --test src/dashboard/rollup.test.js
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, symlinkSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import esbuild from 'esbuild'
import { JSDOM } from 'jsdom'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '..', '..')

// Some modules touch window at import time — give them a DOM first.
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' })
globalThis.window = dom.window
globalThis.document = dom.window.document

const bundleDir = mkdtempSync(join(tmpdir(), 'rollup-test-'))
symlinkSync(join(root, 'node_modules'), join(bundleDir, 'node_modules'), 'dir')
process.on('exit', () => rmSync(bundleDir, { recursive: true, force: true }))
const out = join(bundleDir, 'bundle.mjs')
const entry = `export { isFinished } from ${JSON.stringify(join(root, 'src/dashboard/rollup.js'))}`
await esbuild.build({
    stdin: { contents: entry, resolveDir: root, loader: 'jsx' },
    bundle: true,
    format: 'esm',
    platform: 'node',
    external: ['react', 'react-dom', 'react-dom/*', '@mui/*', '@emotion/*'],
    outfile: out,
    loader: { '.js': 'jsx' },
    logLevel: 'silent',
})
const { isFinished } = await import(pathToFileURL(out).href)

test('isFinished treats blocked and delivery-failed as terminal, like done', () => {
    assert.equal(isFinished('blocked'), true)
    assert.equal(isFinished('delivery-failed'), true)
    assert.equal(isFinished('done'), true)
    assert.equal(isFinished('running'), false)
})
