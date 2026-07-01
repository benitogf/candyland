import { domain, ssl } from '../config'

// One-click copy-reference: build a stable, pasteable reference to a task / quest
// / campaign item. The reference resolves back to that item's stored snapshot via
// GET /api/reference/{kind}/{id} — the server's referenceCollections (internal/
// httpapi/reference.go) maps these same kinds to their storage collections, so a
// copied reference always resolves to the run's stored data.
//
// The dashboard opens items by kind 'run' | 'quest' | 'campaign'; the reference
// handle labels a run as 'task' (its UI name). The server accepts both 'task' and
// 'run' for runs, so either resolves.
const REFERENCE_KIND = { run: 'task', quest: 'quest', campaign: 'campaign' }

export const referenceKind = (kind) => REFERENCE_KIND[kind] || kind

const scheme = () => (ssl ? 'https' : 'http')

// The resolvable URL for an item. Fetching it returns the item's stored JSON —
// the same snapshot the item's dashboard page reads — so it is usable from a
// VSCode Claude session with a plain fetch/WebFetch.
export const referenceUrl = (kind, id) => `${scheme()}://${domain}/api/reference/${referenceKind(kind)}/${id}`

// The one-line reference copied to the clipboard: a human handle plus the
// resolvable URL, ready to paste into a Claude session.
export const referenceText = (kind, id) => `candyland ${referenceKind(kind)} ${id} — ${referenceUrl(kind, id)}`
