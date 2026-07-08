// Render test for the IncidentsBlock audit surface: the self-acknowledged-incident
// trail (non-terminal INCIDENT self-reports) must render its notes when present and
// render nothing when absent — mirroring EscalationsBlock's empty-guard so an
// unconditional render at each detail site is a safe no-op on records with no
// incidents. Field names come straight from internal/run/types.go IncidentNote.
//
// Same self-contained harness as AgentsPanel.test.js: esbuild transforms the JSX,
// jsdom supplies window (config.js reads it at import time), react-dom/server
// renders without a DOM. Run with:  node --test src/components/AuditBlocks.test.js
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

const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' })
globalThis.window = dom.window
globalThis.document = dom.window.document

// Build the bundle under the OS temp dir so no artifact lands in the repo tree.
// react/react-dom/@mui/@emotion stay external (they dynamic-require node builtins,
// which can't be bundled to ESM), so node resolves them relative to the bundle's
// location — symlink the repo's node_modules beside it so that resolution succeeds.
// ('dir'/junction type keeps symlinkSync working on Windows, where it is otherwise
// privileged; harmless on POSIX.) Clean the temp dir on exit so nothing lingers.
const tmp = mkdtempSync(join(tmpdir(), 'auditblocks-test-'))
symlinkSync(join(root, 'node_modules'), join(tmp, 'node_modules'), 'dir')
process.on('exit', () => rmSync(tmp, { recursive: true, force: true }))
const out = join(tmp, 'bundle.mjs')
const entry = `
import React from 'react'
import { renderToString } from 'react-dom/server'
import createCache from '@emotion/cache'
import { CacheProvider } from '@emotion/react'
import { IncidentsBlock } from ${JSON.stringify(join(root, 'src/components/AuditBlocks.js'))}
export const render = (incidents) => renderToString(
    React.createElement(CacheProvider, { value: createCache({ key: 'css' }) },
        React.createElement(IncidentsBlock, { incidents })))
`
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
const { render } = await import(pathToFileURL(out).href)

test('IncidentsBlock renders nothing when there are no incidents', () => {
    assert.equal(render(undefined), '')
    assert.equal(render([]), '')
})

test('IncidentsBlock renders each incident note with its fields', () => {
    const html = render([
        { agent: 'coder-1', summary: 'retried a flaky fetch', detail: 'succeeded on the second attempt', severity: 'warn', at: '2026-07-08T00:00:00Z' },
        { agent: 'coder-2', summary: 'ignored a rule I was given', severity: 'error' },
        { agent: 'coder-3', summary: 'repaired stale lockfile' },
    ])
    // React SSR inserts a <!-- --> text-boundary comment between the label and count.
    assert.match(html, /incidents ·/)
    assert.match(html, /retried a flaky fetch/)
    assert.match(html, /succeeded on the second attempt/)
    assert.match(html, /coder-1/)
    assert.match(html, /warn/)
    // the error-severity branch (severityColor → error.main) must render its label
    assert.match(html, /error/)
    assert.match(html, /ignored a rule I was given/)
    assert.match(html, /repaired stale lockfile/)
    assert.match(html, /2026-07-08T00:00:00Z/)
})

test('IncidentsBlock tolerates a note missing every optional field', () => {
    assert.doesNotThrow(() => render([{ summary: 'bare note' }]))
})
