import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Chip from '@mui/material/Chip'
import Divider from '@mui/material/Divider'
import Step from '@mui/material/Step'
import StepContent from '@mui/material/StepContent'
import StepLabel from '@mui/material/StepLabel'
import Stepper from '@mui/material/Stepper'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'

import MermaidDiagram from '../components/MermaidDiagram'
import Section, { DiagramCard, SpecNote } from '../components/Section'

// ── Diagram definitions ──────────────────────────────────────────────────────

const ARCHITECTURE = `
flowchart TB
  U["🧑‍💻 You — monitor · review the PR"]:::you

  subgraph DASH["🍬 Candyland dashboard"]
    direction LR
    BD["Board"] --- AG["Agents"]
  end

  subgraph CORE["Orchestrator · Go — wraps detritus skills"]
    direction LR
    CO["Conductor<br/>routes the flow"]
    EVT["Event log<br/>ooo realtime state"]
  end

  subgraph FEAT["Per feature"]
    TL["🧭 Tech lead<br/>partition · spawn · integrate"]
    subgraph CODERS["Parallel coders · isolated worktrees"]
      direction LR
      TE["Test eng"]
      BE["Backend"]
      FE["Frontend"]
    end
  end

  PR["🌳 one feature branch → one PR"]

  U <-->|"monitor · review"| DASH
  DASH <-->|"WebSocket · ooo client"| EVT
  EVT --- CO
  CO -->|"settled plan"| TL
  TL -->|"partition + failing tests (emit)"| CO
  CO -->|"spawns each coder"| TE & BE & FE
  TE & BE & FE -->|"events: status · tokens · done"| EVT
  TE & BE & FE -->|"green diffs"| TL
  TL -->|"integrate sequentially + self-review"| PR
  PR -->|review| U

  classDef you fill:#ff5fa2,stroke:#ff5fa2,color:#150d20,font-weight:bold;
`

const PLAIN = `
flowchart LR
  YOU["🧑 You<br/>say what you want"] --> P["💬 Planner<br/>asks a few simple questions"]
  P --> L["🧭 Lead<br/>splits the job into independent pieces"]
  L --> T["👥 Team<br/>builds each piece and proves it works"]
  T --> L2["🧭 Lead<br/>fits the pieces together, double-checks"]
  L2 --> DONE["✅ one finished change<br/>you review"]:::hl
  classDef hl fill:#4be3c0,stroke:#4be3c0,color:#10231f,font-weight:bold;
`

const FLOWS = `
flowchart LR
  G["🎯 a goal"] --> P["📝 open Q&A intake<br/>(/plan-style)"]
  P --> PLAN["📄 settled plan<br/>.plan contract"]:::hl
  PLAN --> B["🧭 parallel build<br/>tech lead + coders"]
  B --> INT["🔗 integrate<br/>+ self-review"]
  INT --> PR["🌳 one PR<br/>you review"]
  classDef hl fill:#4be3c0,stroke:#4be3c0,color:#10231f,font-weight:bold;
`

const JOURNEY = `
sequenceDiagram
  actor You
  participant D as Dashboard
  participant O as Conductor
  participant TL as 🧭 Tech lead
  participant C as Coders (parallel)
  Note over O: open Q&A intake settles scope into a .plan contract
  You->>D: create a session, state a goal
  D->>O: start session
  loop plan until scope is settled
    O-->>D: a question (open Q&A)
    D-->>You: shown live in the session
    You-->>D: your answer
    D->>O: relay
  end
  O-->>D: settled plan + acceptance criteria (.plan contract)
  Note over You,O: once scope is settled, the build runs to one PR
  O->>TL: hand off the settled plan
  TL-->>O: fork-safe partition + a failing test per task (emit, don't spawn)
  O->>C: spawn each coder as its own process (its task + context slice)
  loop each coder, in its own worktree
    C-->>D: events — status · tokens · green/blocked
  end
  C-->>TL: worktree diffs, green
  Note over TL: integrate sequentially · loop back to the owning coder on a dirty merge · self-review until clean
  TL->>O: integrated diff, clean
  O->>D: one PR opened
  D-->>You: review the PR
`

const TECHLEAD = `
sequenceDiagram
  participant TL as 🧭 Tech lead
  participant TE as Test eng
  participant BE as Backend
  participant FE as Frontend
  participant R as Reviewer /gh
  Note over TL,FE: under candyland (out-of-process) the tech lead emits the partition and the conductor spawns each coder as a process — under /forge (in-process) the coders are sub-agents
  Note over TL: partition the feature by file and module — the fork-safe gates
  TL->>TE: write the failing tests that define each task
  TE-->>TL: red tests — the contract
  par each coder in its own worktree
    TL->>BE: implement the backend slice, make tests green
  and
    TL->>FE: implement the frontend slice, make tests green
  end
  BE-->>TL: worktree A diff, green
  FE-->>TL: worktree B diff, green
  Note over TL: integrate worktrees sequentially, resolve conflicts, re-run tests
  TL->>R: self-review the integrated diff
  R-->>TL: clean
  TL->>R: open one PR
`

const STATES = `
stateDiagram-v2
  [*] --> Idle
  Idle --> Working: assigned a task + a failing test
  Working --> Blocked: waiting on a dependency
  Blocked --> Working: dependency done
  Working --> Green: tests pass
  Green --> Integrating: handed to the tech lead
  Integrating --> Working: conflict or blocker
  Integrating --> Done: integrated · PR opened
  Done --> [*]
`

const CONTEXT = `
xychart-beta
  title "Context carried per session — one big session vs distributed agents"
  x-axis ["Monolith", "Tech lead", "Test eng", "Backend", "Frontend", "Reviewer"]
  y-axis "Tokens in context (k)" 0 --> 200
  bar [185, 35, 30, 45, 45, 32]
`

const HANDOFF = `
gantt
  title One feature across the tech lead and coders (relative time units)
  dateFormat X
  axisFormat %s
  section Tech lead
  partition the work          :tl1, 0, 2
  integrate + resolve          :crit, tl2, after be1 fe1, 3
  section Test eng
  write failing tests          :te1, after tl1, 2
  section Backend
  implement (worktree A)       :be1, after te1, 5
  section Frontend
  implement (worktree B)       :fe1, after te1, 5
  section Reviewer
  self-review + open PR         :r1, after tl2, 2
`

// ── Page content ─────────────────────────────────────────────────────────────

const pillars = [
    {
        emoji: '👁️',
        title: 'Observe',
        body: 'See every session, agent, and token budget live. The dashboard is the single source of truth for what is being worked on right now.',
    },
    {
        emoji: '✂️',
        title: 'Distribute',
        body: 'A tech lead partitions each feature and hands each coder only its slice — small context, sharp focus, lower cost.',
    },
    {
        emoji: '🔗',
        title: 'Coordinate',
        body: 'Coders run in parallel, then the tech lead integrates their worktrees sequentially so nothing collides on the way to one PR.',
    },
]

const concepts = [
    {
        title: 'Conductor',
        body: 'Routes a goal into the open Q&A intake, runs the interactive planning loop, then spawns the tech lead and each coder as processes and owns realtime state. The dashboard talks to it.',
    },
    {
        title: 'Plan contract — the seam',
        body: 'Planning ends at a .plan contract: feature spec, acceptance criteria, user-stated rules, and decisions made on your behalf. That single artifact is the only thing that crosses from planning into the build, so nothing is lost at the handoff.',
    },
    {
        title: 'Tech lead',
        body: 'Per feature: partitions the work by file/module and emits the plan; the conductor spawns each coder as a process (emit-don\'t-spawn). The tech lead then integrates their worktrees and opens the PR. The most safety-critical role.',
    },
    {
        title: 'Role coders',
        body: 'Backend / frontend specialists. Each gets a focused context slice plus a failing test, and works alone in its own git worktree.',
    },
    {
        title: 'Test-first contract',
        body: 'The test engineer writes the failing test that defines each task. "Done" means that test goes green — the same TDD gate your /todo-work and /smith already enforce.',
    },
    {
        title: 'Fork-safe partition',
        body: 'The tech lead splits work using detritus\'s fork-safe gates: disjoint files and modules, no overlapping evidence lines, no cross-dependency.',
    },
    {
        title: 'Worktree isolation',
        body: 'Every coder is on its own checkout, so parallel work cannot stomp on shared files. The tech lead is the only one who integrates.',
    },
    {
        title: 'Event stream',
        body: 'Each agent is a headless Claude Code process run with --output-format stream-json. The conductor parses that stream — text, tool calls, token usage, result — adds the lifecycle state it derives itself (green = the task\'s defining test passing, which the conductor runs), and writes it into ooo. The dashboard subscribes over WebSocket. No agent has to self-report its status.',
    },
    {
        title: 'Context slice',
        body: 'An agent prompt is just its task plus the artifacts it depends on. No shared mega-context. This is the main cost and quality lever.',
    },
]

const mapping = [
    ['/plan', 'The planning intake — an open Q&A conversation that settles scope into a .plan contract.'],
    ['/vibe', 'The terminal-only autonomous pipeline: dream + /smith, driven all the way to an open PR (single-threaded), with no dashboard. Candyland does not call it.'],
    ['/forge · /smith', '/forge drives the parallel tech-lead + coders loop in-process; /smith is the fused single-threaded /plan + build + audit. Candyland is the out-of-process driver over the same loop and calls neither.'],
    ['roles/tech-lead', 'Partition by fork-safe gates, drive test-first coders, integrate sequentially, deliver one PR. Emits the partition; the driver spawns the coders.'],
    ['roles/coder-*', 'Test-engineer / backend / frontend behaviors (each composes core/coder)'],
    ['core/build · core/planning', 'Shared build unit + delivery, and the .plan contract that hands a plan to the loop'],
    ['/gh · /gh-self-review', 'Review convergence and one-PR delivery'],
    ['ooo', 'Realtime state + WebSocket event stream behind the dashboard'],
    ['git worktrees', 'Per-coder isolation so parallel agents never collide'],
]

const steps = [
    { label: 'detritus launches the run', body: 'detritus settles the work and starts a run over REST (POST /api/runs → begin). No workspace setup in the dashboard — the run uses the folders detritus hands it.' },
    { label: 'Candyland takes it from here', body: 'The conductor spawns the tech lead and coders and streams their live state to the dashboard — you watch, audit, and stop here.' },
    { label: 'The tech lead takes over', body: 'For each feature, a tech lead partitions the work by file/module and a test engineer writes the failing tests that define each task.' },
    { label: 'Coders run in parallel', body: 'Backend and frontend coders work simultaneously, each in its own worktree, each making its tests green. You watch them flow on the Board.' },
    { label: 'The tech lead integrates', body: 'It pulls the worktrees together sequentially, resolves conflicts, re-runs the tests, and runs a self-review until the diff reads clean.' },
    { label: 'You get one PR', body: 'Candyland opens a single pull request for the feature. Reviewing and merging stay your call — that is the safety floor.' },
]

const priorArt = [
    ['Vibe Kanban', 'Kanban board to run/queue many coding agents', 'Task-board mental model; minimal dashboard'],
    ['Conductor / Crystal', 'Desktop apps running parallel Claude Code in git worktrees', 'Worktree-per-agent isolation'],
    ['Claude Squad', 'tmux-based manager for multiple Claude/Codex sessions', 'Lightweight session multiplexing'],
    ['CrewAI / LangGraph', 'Multi-agent frameworks (roles / state graph)', 'Role specialization + a coordinating lead'],
    ['Temporal / Inngest', 'Durable workflow engines with state dashboards', 'Durable task state; observable runs'],
    ['Langfuse / AgentOps', 'Agent tracing & token/cost dashboards', 'What to visualize: traces, tokens, cost'],
    ['agent-deck (the one you saw)', 'Full-featured agent deck', 'Avoid: too many moving parts for solo use'],
]

const roadmap = [
    ['Phase 0 — now', 'This PR: the spec. Dashboard shell + this "How it works" page.'],
    ['Phase 1', 'Go conductor + ooo event stream. Run the interactive planning loop from the dashboard. The Agents view goes live.'],
    ['Phase 2', 'Drive the implementation loop: the conductor spawns the tech lead and coders (detritus roles, already written) as processes against a settled plan. The Board renders the live task DAG.'],
    ['Phase 3', 'Full parallel build visualized as a live hierarchy — fork-safe partition, role coders in worktrees, test-first, sequential integration with loop-back on dirty merges.'],
    ['Phase 4 — maybe', 'pivot-based multi-machine sync, if a single host stops being enough.'],
]

const Grid3 = ({ children }) => (
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: 'repeat(3, 1fr)' }, gap: 2 }}>
        {children}
    </Box>
)

const RUNNER_CMD = `env -C /path/to/worktree claude \\
  -p "<this coder's single task + only the context it depends on>" \\
  --model claude-opus-4-8 \\
  --effort high \\
  --strict-mcp-config \\
  --mcp-config '{"mcpServers":{"detritus":{"command":"detritus","args":[]}}}'`

const DeveloperGuide = () => (
    <Box>
        {/* Hero */}
        <Box sx={{ mb: 5 }}>
            <Chip label="the spec · read me first" color="primary" variant="outlined" sx={{ mb: 2 }} />
            <Typography variant="h3" gutterBottom>
                Conduct many agents. Watch all of it. 🍬
            </Typography>
            <Typography variant="h6" color="text.secondary" sx={{ fontWeight: 400, maxWidth: 840 }}>
                Candyland is a solo orchestration sidecar. detritus launches a run over REST;
                a tech lead then splits each feature across focused coders running in parallel, integrates their
                work, and candyland shows everything live — so you stop juggling sessions by hand, watch and audit
                the build here, and review one finished PR instead.
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: 'italic' }}>
                This page is the blueprint we build against. Candyland is the dashboard; every agent behavior
                is a detritus skill it wraps.
            </Typography>
        </Box>

        <Grid3>
            {pillars.map((p) => (
                <Card key={p.title}>
                    <CardContent>
                        <Typography variant="h4" component="div" sx={{ mb: 1 }}>
                            {p.emoji}
                        </Typography>
                        <Typography variant="h6" color="primary" gutterBottom>
                            {p.title}
                        </Typography>
                        <Typography variant="body2" color="text.secondary">
                            {p.body}
                        </Typography>
                    </CardContent>
                </Card>
            ))}
        </Grid3>

        <Divider sx={{ my: 6 }} />

        {/* 1. Architecture */}
        <Section
            kicker="the big picture"
            title="How the pieces fit"
            intro="In plain terms: detritus settles what you want, a lead splits the job into independent pieces, a team builds each piece and proves it works, the lead fits them together and double-checks, and you get one finished change to review. Candyland is the screen that shows all of this live."
        >
            <DiagramCard caption="You describe it; a lead and a small team build it; you review one finished change.">
                <MermaidDiagram chart={PLAIN} />
            </DiagramCard>
            <SpecNote>
                Under the hood: detritus launches a run over REST and the dashboard mirrors the orchestrator's
                state over a WebSocket. Once a plan is settled, a per-feature tech lead spawns parallel coders in
                isolated git worktrees, integrates their work, and ships one PR. Every agent is a process Candyland
                launches and watches — the behaviors themselves are detritus skills.
            </SpecNote>
            <DiagramCard caption="The same flow, drawn for engineers: goal in, one PR out, every event mirrored to the dashboard in between.">
                <MermaidDiagram chart={ARCHITECTURE} />
            </DiagramCard>
        </Section>

        {/* 2. Plan, then build */}
        <Section
            kicker="planning, then building"
            title="Plan, then build"
            intro="Planning is separated from building. You plan through an open Q&A conversation that settles scope into a .plan contract; the parallel implementation loop (a tech lead + coders) then builds that contract into one PR. The plan is the seam: it is the only thing that crosses from planning into the build."
        >
            <DiagramCard caption="A single intake settles one plan; the implementation loop builds it to a single PR.">
                <MermaidDiagram chart={FLOWS} />
            </DiagramCard>
            <SpecNote>
                Where the build runs is a driver choice over the <em>same</em> detritus roles. Candyland is the
                out-of-process driver: it spawns the tech lead and each coder as their own process it watches —
                the parallel loop — with a single <strong>Stop run</strong> control that halts the whole flow.
                (Lean by design: no per-agent stop and no resume — you observe everything and halt the run if you
                need to.) At a terminal you can run the identical loop in-process with <strong>/forge</strong>
                (coders as sub-agents, no dashboard). The terminal-only autonomous path is <strong>/vibe</strong>:
                it bundles the dream intake with <strong>/smith</strong> and drives all the way to an open PR by
                itself — but single-threaded and with no dashboard. <strong>/smith</strong> is the fused
                single-threaded /plan + build + audit. Candyland calls none of /vibe, /forge, or /smith — it
                drives the shared roles directly so it keeps the per-agent control only the out-of-process driver
                gives.
            </SpecNote>
        </Section>

        {/* 3. The whole journey */}
        <Section
            kicker="goal in, one PR out"
            title="The whole loop, start to finish — inside the dashboard"
            intro="It doesn't stop at a plan. detritus settles scope and launches the run; then candyland shows the tech lead partition the work, the coders build it in parallel, the tech lead integrate and self-review, and one PR open for you to review — all live."
        >
            <DiagramCard caption="One session, end to end: detritus launches the run, you watch the parallel build, review one PR. Planning settles scope; then the build runs to one PR.">
                <MermaidDiagram chart={JOURNEY} />
            </DiagramCard>
            <SpecNote>
                detritus settles scope and starts the run over REST; the conductor then hands off to the tech lead
                and the build runs to a single PR without further prompts. The dashboard observes every event over
                a WebSocket; reviewing and merging stay your call.
            </SpecNote>
        </Section>

        {/* 4. Core concepts */}
        <Section kicker="vocabulary" title="The ideas the whole system rests on">
            <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)' }, gap: 2 }}>
                {concepts.map((c) => (
                    <Card key={c.title}>
                        <CardContent>
                            <Typography variant="subtitle1" color="secondary" gutterBottom sx={{ fontWeight: 700 }}>
                                {c.title}
                            </Typography>
                            <Typography variant="body2" color="text.secondary">
                                {c.body}
                            </Typography>
                        </CardContent>
                    </Card>
                ))}
            </Box>
        </Section>

        {/* 5. Inside a feature: the tech lead */}
        <Section
            kicker="inside a feature"
            title="The tech lead splits, spawns, and integrates"
            intro="This is the heart of it. A tech lead partitions a feature into non-overlapping slices, a test engineer turns each slice into a failing test, role coders make those tests green in parallel, and the tech lead stitches the worktrees back together into one reviewed PR."
        >
            <DiagramCard>
                <MermaidDiagram chart={TECHLEAD} />
            </DiagramCard>
            <SpecNote>
                Two separate axes — don't conflate them. <strong>Roles</strong> (backend / frontend / test) are
                context and skill specializations. <strong>Conflict avoidance</strong> is the file/module
                partition (the fork-safe gates). A backend change and a frontend change rarely collide; two
                backend changes can. The partition, not the role, is what keeps coders out of each other's way.
            </SpecNote>
            <SpecNote>
                Highest-risk stage, drawn honestly: parallel coders <em>plus</em> a tech lead doing sequential
                merge-resolution means two fallible agent steps — the partition decision and the integration.
                Integration re-runs the full test suite and a self-review; if the merge is dirty, the affected
                coder loops back rather than the tech lead hand-fixing silently.
            </SpecNote>
        </Section>

        {/* 6. Agent state machine */}
        <Section
            kicker="one coder's life"
            title="Every coder moves through the same states"
            intro="The Agents view colors each running session by its state, so a glance tells you who's building, who's green, who's blocked, and what's being integrated."
        >
            <DiagramCard>
                <MermaidDiagram chart={STATES} />
            </DiagramCard>
            <SpecNote>
                Open a run in the <strong>Dashboard</strong> and check the <strong>Agents</strong> tab for a live view of this — every agent's
                state and its real-time output side by side. The state is not self-reported: the conductor derives
                it from each process's <code>stream-json</code> stream plus the test runs it owns (<code>green</code>
                = the defining test passing). That, plus a small structured <em>partition</em> emitted by the tech
                lead for the task DAG, is everything observability needs — no extra detritus surface.
            </SpecNote>
        </Section>

        {/* 7. Context economy */}
        <Section
            kicker="why this is cheaper"
            title="Small context per agent, not one giant session"
            intro="A single session that does everything drags an ever-growing context behind it. Candyland gives the tech lead, the test engineer, and each coder only their slice — so the feature gets built with a fraction of the context, and each agent stays sharp."
        >
            <DiagramCard caption="Illustrative. The monolith carries everything; each distributed agent carries only its slice.">
                <MermaidDiagram chart={CONTEXT} />
            </DiagramCard>
            <SpecNote>
                Context assembly is the tech lead's job: when it spawns a coder it builds the prompt from that
                coder's task + the failing test + the artifacts it depends on — nothing else.
            </SpecNote>
        </Section>

        {/* 8. Coordination */}
        <Section
            kicker="staying out of each other's way"
            title="Coordination and handoff over time"
            intro="The test engineer defines the contract first. Independent coders then run in parallel in separate worktrees. The tech lead's integration is the one sequential step — it waits for every coder to go green before merging and reviewing."
        >
            <DiagramCard>
                <MermaidDiagram chart={HANDOFF} />
            </DiagramCard>
        </Section>

        <Divider sx={{ my: 6 }} />

        {/* How to use */}
        <Section kicker="how you use it" title="From one sentence to a pull request">
            <Stepper orientation="vertical" sx={{ '& .MuiStepLabel-label': { fontWeight: 700 } }}>
                {steps.map((s) => (
                    <Step key={s.label} active expanded completed={false}>
                        <StepLabel>{s.label}</StepLabel>
                        <StepContent>
                            <Typography variant="body2" color="text.secondary" sx={{ pb: 1 }}>
                                {s.body}
                            </Typography>
                        </StepContent>
                    </Step>
                ))}
            </Stepper>

            <Box sx={{ mt: 3 }}>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                    Under the hood, each coder is exactly the headless runner you'd type by hand — the tech lead
                    just fills in the task, the failing test, the context slice, and the worktree path:
                </Typography>
                <Box
                    component="pre"
                    sx={{
                        m: 0,
                        p: 2,
                        borderRadius: 2,
                        overflowX: 'auto',
                        fontSize: 13,
                        lineHeight: 1.6,
                        backgroundColor: '#0d0916',
                        border: '1px solid',
                        borderColor: 'divider',
                        color: '#d9ccff',
                    }}
                >
                    {RUNNER_CMD}
                </Box>
            </Box>
        </Section>

        {/* Mapping to stack */}
        <Section
            kicker="built on what you already have"
            title="How it maps to your stack"
            intro="Candyland is the sidecar — detritus launches a run over REST, and it spawns each agent as a process it watches, then visualizes state for you to monitor, audit, and stop. It contains no agent logic of its own: every behavior below is a detritus skill it invokes via kb_get."
        >
            <Card sx={{ overflowX: 'auto' }}>
                <Table size="small" sx={{ minWidth: 560 }}>
                    <TableHead>
                        <TableRow>
                            <TableCell sx={{ fontWeight: 700 }}>detritus provides</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>Role in Candyland</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {mapping.map(([a, b]) => (
                            <TableRow key={a}>
                                <TableCell sx={{ color: 'secondary.main', fontFamily: 'monospace', whiteSpace: 'nowrap' }}>{a}</TableCell>
                                <TableCell sx={{ color: 'text.secondary' }}>{b}</TableCell>
                            </TableRow>
                        ))}
                    </TableBody>
                </Table>
            </Card>
            <SpecNote>
                These roles now live in detritus: <code>roles/tech-lead</code>, <code>roles/coder-*</code>,
                <code>core/build</code>, <code>core/coder</code>, and the <code>/forge</code> driver — all
                composing one shared build contract. Candyland is the <strong>out-of-process driver</strong> over
                them: it owns spawning, monitoring, and visualizing the agents (process lifecycle); detritus owns
                what each agent does (decisions + choreography). The split is what keeps the dashboard a slim
                coordination layer holding no skills of its own.
            </SpecNote>
        </Section>

        {/* Prior art */}
        <Section
            kicker="we're not starting from scratch"
            title="Prior art — what we borrow, what we avoid"
            intro="Comparable tools exist; the point of Candyland is the lightweight, solo-first subset. (This table is being refined by a background research pass.)"
        >
            <Card sx={{ overflowX: 'auto' }}>
                <Table size="small" sx={{ minWidth: 560 }}>
                    <TableHead>
                        <TableRow>
                            <TableCell sx={{ fontWeight: 700 }}>Tool</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>What it is</TableCell>
                            <TableCell sx={{ fontWeight: 700 }}>What we take from it</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {priorArt.map(([a, b, c]) => (
                            <TableRow key={a}>
                                <TableCell sx={{ color: 'secondary.main', whiteSpace: 'nowrap' }}>{a}</TableCell>
                                <TableCell sx={{ color: 'text.secondary' }}>{b}</TableCell>
                                <TableCell sx={{ color: 'text.secondary' }}>{c}</TableCell>
                            </TableRow>
                        ))}
                    </TableBody>
                </Table>
            </Card>
        </Section>

        {/* Roadmap */}
        <Section kicker="where this goes" title="Build order">
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.5 }}>
                {roadmap.map(([phase, what]) => (
                    <Card key={phase}>
                        <CardContent sx={{ display: 'flex', gap: 2, alignItems: 'baseline', py: 1.5, '&:last-child': { pb: 1.5 } }}>
                            <Typography variant="subtitle2" color="primary" sx={{ minWidth: 130, fontWeight: 700 }}>
                                {phase}
                            </Typography>
                            <Typography variant="body2" color="text.secondary">
                                {what}
                            </Typography>
                        </CardContent>
                    </Card>
                ))}
            </Box>
        </Section>
    </Box>
)

const HowItWorks = () => (
    <Box>
        <DeveloperGuide />
    </Box>
)

export default HowItWorks
