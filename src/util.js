// Small shared helpers.

// URL-safe slug from arbitrary text (for branch names).
export const slug = (s) => (s
    ? s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '').slice(0, 32)
    : '')

// A short, human title suggested from the prompt — the title is optional and is
// NOT part of what's sent to the agent, so we derive a label for display. Strips
// slash commands and takes the first handful of words.
export const suggestTitle = (prompt) => {
    const firstLine = (prompt || '').split('\n').find((l) => l.trim()) || ''
    const words = firstLine.replace(/\/[\w-]+/g, '').trim().split(/\s+/).filter(Boolean).slice(0, 7)
    if (!words.length) return ''
    const s = words.join(' ')
    return s.charAt(0).toUpperCase() + s.slice(1)
}

// The label to show for a run: explicit title, else a suggestion from the
// prompt, else a neutral fallback.
export const runLabel = ({ title, prompt }) => (title?.trim() || suggestTitle(prompt) || 'Untitled run')
