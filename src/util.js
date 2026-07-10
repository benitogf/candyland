// Small shared helpers.

// Drop a leading markdown heading marker (`# ...`) and a leading
// `Objective:`/`Goal:` label so a structured objective derives a clean title.
const stripHeading = (line) => line.replace(/^#+\s*/, '').replace(/^(objective|goal)\s*:\s*/i, '')

// A short, human title suggested from the prompt — the title is optional and is
// NOT part of what's sent to the agent, so we derive a label for display. Strips
// a leading heading/label and slash commands, takes the first handful of words.
export const suggestTitle = (prompt) => {
    const firstLine = stripHeading((prompt || '').split('\n').find((l) => l.trim()) || '')
    const words = firstLine.replace(/\/[\w-]+/g, '').trim().split(/\s+/).filter(Boolean).slice(0, 7)
    if (!words.length) return ''
    const s = words.join(' ')
    return s.charAt(0).toUpperCase() + s.slice(1)
}

// The label to show for a run: explicit title, else a suggestion from the
// prompt, else a neutral fallback.
export const runLabel = ({ title, prompt }) => (title?.trim() || suggestTitle(prompt) || 'Untitled run')

// Quest display label mirrors the run model: the backend stamps a short `title`
// at creation; the suggestTitle fallback keeps legacy title-less items from
// rendering a multi-paragraph objective in a title slot.
export const questLabel = (q) => (q.title?.trim() || suggestTitle(q.objective || q.originalObjective) || 'Untitled quest')
