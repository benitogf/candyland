// Shared filtering for the one work/history section. The same filter set is
// applied to whichever level the section is pivoted to (runs · quests ·
// campaigns), so filters carry across a pivot. Filters live in the URL query
// string (see usePivotFilters) so a pivot link preserves them.
//
// The filter keys (all optional, all strings in the URL):
//   q         text search (matches id/title/objective/intent/folder/branch)
//   parent    'none' = no-parent (runs with no questId/campaignId) — this is the
//             DEFAULT so the Work list leads with top-level work and children are
//             reached by drilling into their parent; 'any' = show every item flat
//             (children included); a campaign id (cN) or quest id (qN) filters to
//             that parent's children
//   repo      folder/repo substring
//   status    exact status (running|paused|done|cancelled|stopped|blocked|…)
//   pr        PR state: 'open' (has a PR) | 'none' (no PR)
//   from/to   ISO date bounds (yyyy-mm-dd) against the item's created/updated time

const lc = (s) => String(s || '').toLowerCase()

// Best-effort created/updated timestamp for date filtering. Runs don't carry a
// timestamp field, so they only match when no date bound is set (handled below).
const itemTime = (item) => item.createdAt || item.updatedAt || item.endedAt || ''

// Does this item have an opened PR? Runs/quests/campaigns all expose prUrl
// and/or a prs[] array (campaigns also roll up prsOpened on quests).
const hasPR = (item) => {
    if (item.prUrl) return true
    if (Array.isArray(item.prs) && item.prs.some((p) => p && p.url)) return true
    if (typeof item.prsOpened === 'number' && item.prsOpened > 0) return true
    return false
}

// The repo/folder a row belongs to (folders[0] is the git repo).
export const folderOf = (item) => item.folders?.[0] || ''

// Read the active filter set out of URLSearchParams.
export const readFilters = (params) => ({
    q: params.get('q') || '',
    // Default to no-parent: the Work list leads with top-level runs/quests, and
    // children are reached by drilling into their parent (pick "Any parent" to
    // flatten). Absence of the param means the default, not "show everything".
    parent: params.get('parent') || 'none',
    repo: params.get('repo') || '',
    status: params.get('status') || '',
    pr: params.get('pr') || '',
    from: params.get('from') || '',
    to: params.get('to') || '',
})

// True when only the defaults are active (text is handled separately). The
// default parent is 'none', so it is not itself a narrowing filter — only an
// explicit parent id or 'any' counts.
export const noFilters = (f) => (!f.parent || f.parent === 'none') && !f.repo && !f.status && !f.pr && !f.from && !f.to

// Apply the shared filters to one item. `level` tells us how to read parent
// links: a run's parent is questId/campaignId, a quest's parent is campaignId,
// a campaign has no parent.
export const matchFilters = (item, f, level, textFields) => {
    // text search
    if (f.q) {
        const needle = lc(f.q)
        if (!textFields.some((s) => lc(s).includes(needle))) return false
    }
    // parent: 'none' (the default) surfaces items executed outside any
    // quest/campaign; 'any' flattens (no parent narrowing); a specific id filters
    // to that parent's children.
    if (f.parent === 'none') {
        if (level === 'runs' && (item.questId || item.campaignId)) return false
        if (level === 'quests' && item.campaignId) return false
        // campaigns have no parent — always "no parent", so they pass
    } else if (f.parent && f.parent !== 'any') {
        const parents = [item.questId, item.campaignId].filter(Boolean)
        if (!parents.includes(f.parent)) return false
    }
    // repo / folder
    if (f.repo && !lc(folderOf(item)).includes(lc(f.repo))) return false
    // status
    if (f.status && item.status !== f.status) return false
    // PR state
    if (f.pr === 'open' && !hasPR(item)) return false
    if (f.pr === 'none' && hasPR(item)) return false
    // date bounds — only filter items that carry a timestamp
    if (f.from || f.to) {
        const t = itemTime(item)
        if (!t) return false
        const day = t.slice(0, 10)
        if (f.from && day < f.from) return false
        if (f.to && day > f.to) return false
    }
    return true
}
