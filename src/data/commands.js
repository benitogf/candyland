// Detritus slash commands surfaced for autocomplete in the prompt input —
// the set of user-typable commands the input offers.
export const DETRITUS_COMMANDS = [
    // planning + build
    { cmd: '/plan', desc: 'Analyze requirements and create an implementation plan' },
    { cmd: '/vibe', desc: 'Executive intake → autonomous build → one PR' },
    { cmd: '/forge', desc: 'Drive a settled plan to a PR (parallel tech-lead + coders)' },
    { cmd: '/smith', desc: 'Feature → merged PR → janitor-style audit loop' },
    { cmd: '/janitor', desc: 'Recurring proactive code-maintenance worker' },
    // github
    { cmd: '/gh', desc: 'Router for GitHub issue/PR workflows' },
    { cmd: '/gh-issue-create', desc: 'Draft and post a GitHub issue' },
    { cmd: '/gh-issue-work', desc: 'Take a GitHub issue end-to-end to a PR' },
    { cmd: '/gh-feedback-work', desc: 'Address open PR review feedback' },
    { cmd: '/gh-self-review', desc: 'Deep self-audit of pending changes' },
    { cmd: '/gh-pr', desc: 'Hard-review a GitHub PR' },
    // code + review
    { cmd: '/code', desc: 'Pack-backed code exploration' },
    { cmd: '/code-review', desc: 'Review the current diff for bugs + cleanups' },
    { cmd: '/simplify', desc: 'Reuse / simplification / efficiency cleanups' },
    { cmd: '/review', desc: 'Review a pull request' },
    { cmd: '/security-review', desc: 'Security review of pending changes' },
    { cmd: '/verify', desc: 'Run the app and verify a change works' },
    { cmd: '/run', desc: 'Launch and drive the app' },
    { cmd: '/init', desc: 'Initialize a CLAUDE.md for the codebase' },
    // testing
    { cmd: '/testing', desc: 'Testing workflows index' },
    { cmd: '/flaky-check', desc: 'Flaky test detection' },
    { cmd: '/testing-go-backend-async', desc: 'Deterministic async event tests' },
    { cmd: '/testing-go-backend-e2e', desc: 'Consolidated full-lifecycle E2E tests' },
    { cmd: '/testing-go-backend-mock', desc: 'Minimal boundary mocking' },
    // principles
    { cmd: '/coding-style', desc: 'Self-documenting code rules' },
    { cmd: '/line-of-sight', desc: 'Flat code, early returns' },
    { cmd: '/go-modern', desc: 'Modern Go patterns' },
    { cmd: '/truthseeker', desc: 'Foundational principles (always active)' },
    // project + KB
    { cmd: '/todo', desc: 'Cross-session todo management' },
    { cmd: '/optimize', desc: 'Re-index and optimize KB docs' },
    { cmd: '/grow', desc: 'Learn from conversation corrections' },
    { cmd: '/deep-research', desc: 'Fan-out, fact-checked research report' },
    { cmd: '/sync-docs', desc: 'Sync Obsidian vault with the repo docs' },
    // setup + maintenance
    { cmd: '/detritus-update', desc: 'Update detritus to the latest version' },
    { cmd: '/setup-superpowers', desc: 'Apply baseline Claude Code settings' },
    { cmd: '/setup-extra-rules', desc: 'Generate personalized rule files + hooks' },
    { cmd: '/cleanup-extra-rules', desc: 'Remove detritus-generated rules + hooks' },
    { cmd: '/update-config', desc: 'Configure the Claude Code harness' },
    { cmd: '/fewer-permission-prompts', desc: 'Allowlist common read-only tool calls' },
    { cmd: '/keybindings-help', desc: 'Customize keyboard shortcuts' },
    { cmd: '/loop', desc: 'Run a prompt/command on a recurring interval' },
    { cmd: '/schedule', desc: 'Create scheduled cloud agents (cron)' },
    { cmd: '/claude-api', desc: 'Claude API / SDK reference' },
]
