// Regression test for the events-guard slice: an agent without an events array
// (older persisted data, or an agent that is still planning) must NOT crash the
// per-worker lens. Two defenses are proven together:
//   1. Data layer — normalizeRun coerces every agent's events to an array.
//   2. Render layer — AgentDetail tolerates a raw agent whose events is missing.
//
// There is no unit runner wired into the repo, so the test is self-contained:
// esbuild transforms the JSX modules to a Node-loadable bundle, jsdom supplies
// the browser globals config.js reads at import time, and react-dom/server
// renders without a DOM. Run with:  node --test src/components/AgentsPanel.test.js
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import esbuild from 'esbuild'
import { JSDOM } from 'jsdom'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '..', '..')

// config.js touches window.location at import time — give it a DOM first.
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' })
globalThis.window = dom.window
globalThis.document = dom.window.document

// Bundle the real component + the real normalizeRun through esbuild (JSX loader),
// then load it as ESM. Bundling keeps react/mui/config in one graph so the
// modules under test run exactly as they ship.
// Emit the bundle inside the project so Node resolves the external react/mui
// imports against the repo's own node_modules.
const out = join(mkdtempSync(join(root, '.agentspanel-test-')), 'bundle.mjs')
const entry = `
import React from 'react'
import { renderToString } from 'react-dom/server'
import createCache from '@emotion/cache'
import { CacheProvider } from '@emotion/react'
import AgentsPanel from ${JSON.stringify(join(root, 'src/panels/AgentsPanel.js'))}
export { normalizeRun } from ${JSON.stringify(join(root, 'src/data/ooo.js'))}
// A fresh emotion cache satisfies MUI's styled components during SSR.
export const render = (agents) => renderToString(
    React.createElement(CacheProvider, { value: createCache({ key: 'css' }) },
        React.createElement(AgentsPanel, { agents })))
`
await esbuild.build({
    stdin: { contents: entry, resolveDir: root, loader: 'jsx' },
    bundle: true,
    format: 'esm',
    platform: 'node',
    // Keep react/mui external so Node loads them natively (bundling react-dom's
    // server build breaks on its dynamic requires); bundle everything else
    // (ooo-client and its CJS deps) so esbuild handles the interop.
    external: ['react', 'react-dom', 'react-dom/*', '@mui/*', '@emotion/*'],
    outfile: out,
    loader: { '.js': 'jsx' },
    logLevel: 'silent',
})
const { render, normalizeRun } = await import(pathToFileURL(out).href)

test('AgentDetail renders a raw agent whose events array is missing', () => {
    const agent = { id: 'a1', role: 'coder', emoji: '🤖', state: 'running', worktree: 'wt', model: 'opus' }
    assert.doesNotThrow(() => render([agent]))
})

test('normalizeRun coerces every agent to an events array', () => {
    const run = normalizeRun({ id: 'r1', agents: [{ id: 'a1' }, { id: 'a2', events: [{ t: 'text', text: 'hi' }] }] })
    assert.ok(Array.isArray(run.agents[0].events))
    assert.equal(run.agents[0].events.length, 0)
    assert.equal(run.agents[1].events.length, 1)
    assert.ok(Array.isArray(run.tasks))
})

test('normalizeRun tolerates a null run and a null agents list', () => {
    assert.equal(normalizeRun(null), null)
    assert.deepEqual(normalizeRun({ id: 'r2', agents: null }).agents, [])
})
