// Package conductor owns run lifecycle: it creates runs, drives them with the
// real headless claude executor, and publishes every state change into ooo so
// the UI reads live data. There is no mock and no scripted/demo fallback — this
// is the single source of truth, and a run only ever reflects real agent work.
package conductor

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/candyland/internal/run"
	"github.com/benitogf/ooo"
)

// Executor drives a run from planning to PR, calling Update to publish state.
type Executor interface {
	// Execute blocks until the run is done or the control channel says stop.
	// Candyland keeps a lean, flow-level control surface: it must honor "stop"
	// and "restart" on control (no resume — a stopped run is restarted).
	Execute(c *Conductor, id string, control <-chan string)
	Name() string
}

type runtime struct {
	mu sync.Mutex
	r  run.Run
	// control is the CURRENT executor's command channel (stop/restart/quit). It's
	// swapped for a fresh one (under mu) each time an executor is (re)spawned, so a
	// command can never cross from a terminated executor to its replacement.
	control   chan string
	cancelled bool // set by Cancel — a cancelled run must never publish again
	// Planning questions are generated once per run and cached, so a refresh or
	// retry reuses them (deterministic + one Claude call) instead of regenerating.
	questions     []run.Question
	questionsDone bool
}

// Conductor is the orchestrator. Safe for concurrent use.
type Conductor struct {
	server *ooo.Server
	mu     sync.Mutex
	runs   map[string]*runtime
	seq    int
	// folders resolves a run's working folders. Defaults to the folders the run
	// was launched with (Spec.Folders, carried on the Run); tests override it to
	// point a run at a throwaway git repo.
	folders func(r run.Run) ([]string, error)
	// bus is the coordination back-channel (Realization B), set by StartBus.
	// nil when no bus is wired (e.g. serverless tests).
	bus       *bus.Bus
	busAgents map[string]bool // agent ids whose inbox filters are registered
}

// New builds a conductor bound to an ooo server. Every run is driven by the real
// headless `claude` executor; if Claude Code isn't installed or authenticated a
// run fails honestly (see resilience.go) rather than falling back to a demo.
func New(server *ooo.Server) *Conductor {
	c := &Conductor{
		server:    server,
		runs:      map[string]*runtime{},
		busAgents: map[string]bool{},
	}
	c.folders = runFolders
	return c
}

// runFolders resolves a run's working folders from the run itself — they were
// supplied at launch (Spec.Folders → Run.Folders). No workspace lookup: the
// launcher (the VSCode session's cwd) owns the folder set.
func runFolders(r run.Run) ([]string, error) {
	if len(r.Folders) == 0 {
		return nil, fmt.Errorf("run has no folders (the launcher must supply at least the git repo)")
	}
	return r.Folders, nil
}

// publish writes the run object to ooo (key runs/<id>); subscribers update live.
func (c *Conductor) publish(r run.Run) {
	if c.server == nil {
		return // tests construct a serverless conductor and read state via Get
	}
	b, err := json.Marshal(r)
	if err != nil {
		log.Printf("candyland: marshal run %s: %v", r.ID, err)
		return
	}
	// publish is the only write path for run state; a dropped write means the
	// live UI silently stops advancing — surface it (the server is otherwise Silenced).
	if _, err := c.server.Storage.Set("runs/"+r.ID, json.RawMessage(b)); err != nil {
		log.Printf("candyland: publish run %s: %v", r.ID, err)
	}
}

// Update mutates a run under lock, recomputes derived fields, and publishes it.
// Executors call this to stream progress — it's the only write path.
func (c *Conductor) Update(id string, mutate func(*run.Run)) {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// A cancelled run is gone — drop any late executor update so it can't
	// resurrect the deleted ooo key (the executor goroutine may still be winding
	// down its process tree after Cancel removed the run).
	if rt.cancelled {
		return
	}
	mutate(&rt.r)
	recompute(&rt.r)
	// Publish while still holding rt.mu so it's serialized with Cancel: either
	// this write lands before Cancel marks the run cancelled and deletes the key,
	// or Cancel wins and the next Update sees cancelled and skips. Releasing the
	// lock before publishing would let a snapshot be written to ooo AFTER Cancel
	// deleted the key, resurrecting the run as a phantom.
	c.publish(rt.r)
}

// tracked returns the in-memory runtime for id, rehydrating it from ooo when the
// conductor was restarted since the run was created (the map is empty then, but
// the run is still persisted). Without this, controls like Restart/Edit would
// 409 on any run that outlived the process that started it. Returns nil only when
// the run isn't in memory AND isn't in storage.
func (c *Conductor) tracked(id string) *runtime {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt != nil {
		return rt
	}
	if c.server == nil {
		return nil
	}
	obj, err := c.server.Storage.Get("runs/" + id)
	if err != nil {
		return nil
	}
	var r run.Run
	if err := json.Unmarshal(obj.Data, &r); err != nil {
		return nil
	}
	rt = &runtime{r: r, control: newControl()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.runs[id]; existing != nil {
		return existing // another goroutine rehydrated it first
	}
	c.runs[id] = rt
	return rt
}

// Get returns a deep copy of the current run state. The copy must be deep: a
// run's Agents/Tasks (and each Agent's Events) are slices the executor keeps
// mutating under rt.mu, so a shallow copy would share their backing arrays and
// the caller would race those writes the moment it reads them after the unlock.
func (c *Conductor) Get(id string) (run.Run, bool) {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return run.Run{}, false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return cloneRun(rt.r), true
}

// cloneRun deep-copies the slices that get mutated in place so a returned run is
// safe to read without holding rt.mu.
func cloneRun(r run.Run) run.Run {
	agents := make([]run.Agent, len(r.Agents))
	for i, a := range r.Agents {
		a.Events = append([]run.Event(nil), a.Events...)
		agents[i] = a
	}
	r.Agents = agents
	r.Tasks = append([]run.Task(nil), r.Tasks...)
	return r
}

// ReconcileOrphans marks any persisted run left non-terminal by a previous
// process as ended. Run state lives in memory, so after a restart these runs have
// no executor and can't be controlled — without this they'd show as forever
// "planning"/"running" phantoms in the live dashboard. Candyland doesn't resume
// runs, so each is closed with an explanatory error (a clean record of what
// happened, not a fake completion). Must run AFTER server.Start() (storage is
// live only then); runs already in a terminal state (done or cancelled) are
// left untouched as genuine history.
func (c *Conductor) ReconcileOrphans() {
	if c.server == nil {
		return
	}
	keys, err := c.server.Storage.Keys()
	if err != nil {
		log.Printf("candyland: reconcile runs: %v", err)
		return
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "runs/") {
			continue
		}
		obj, err := c.server.Storage.Get(k)
		if err != nil {
			continue
		}
		var r run.Run
		// A cancelled run is already a terminal genuine record (Cancel persists
		// Status=="cancelled" with no Error); rewriting it to "done"/Interrupted
		// would corrupt the user's deliberate-cancel history on restart.
		if json.Unmarshal(obj.Data, &r) != nil || r.Status == "done" || r.Status == "cancelled" {
			continue
		}
		r.Status = "done"
		if r.Error == "" {
			r.Error = "Interrupted — the candyland server restarted, and runs don't resume. Start a new run."
		}
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		if _, err := c.server.Storage.Set(k, json.RawMessage(b)); err != nil {
			log.Printf("candyland: reconcile run %s: %v", r.ID, err)
		}
	}
}

// Create registers a new run (status: planning) and publishes it. The build
// executor is started later via Begin, after the UI's planning Q&A completes.
func (c *Conductor) Create(spec run.Spec) string {
	c.mu.Lock()
	c.seq++
	id := fmt.Sprintf("r%d", c.seq)
	c.mu.Unlock()

	r := run.Run{
		ID:      id,
		Title:   spec.Title,
		Prompt:  spec.Prompt,
		Mode:    spec.Mode,
		Folders: spec.Folders,
		// Include the run id so two runs from the same prompt don't collide on the
		// branch (and therefore on the push / PR head).
		Branch:       runBranch(spec, id),
		Status:       "planning",
		Phase:        0,
		TokensBudget: 900,
		Tasks:        []run.Task{},
		Agents:       []run.Agent{},
		Executor:     "claude",
	}
	rt := &runtime{r: r, control: newControl()}
	c.mu.Lock()
	c.runs[id] = rt
	c.mu.Unlock()
	c.publish(r)
	log.Printf("candyland: run %s created (%s, folders %v)", id, orEmpty(r.Mode, "?"), r.Folders)
	return id
}

// orEmpty returns def when s is empty (small logging helper).
func orEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Begin starts the build executor for a run once planning is done. The planning
// answers (if any) are folded into the prompt that drives the agents.
func (c *Conductor) Begin(id string, answers map[string]any) {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return
	}
	// Idempotent start: only a run still in planning may begin. Atomically
	// check-and-set under rt.mu so a double POST (double-click / retry) can't
	// spawn a second executor goroutine racing on the same control channel.
	rt.mu.Lock()
	if rt.r.Status != "planning" {
		rt.mu.Unlock()
		return
	}
	rt.r.Status = "running"
	ctrl := newControl() // a fresh channel for THIS executor — a stale command (e.g. a quit) left on a prior generation's channel can never reach it
	rt.control = ctrl
	rt.mu.Unlock()

	ex := &ClaudeExecutor{}
	extra := formatAnswers(answers)
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Executor = ex.Name()
		if extra != "" {
			r.Prompt = strings.TrimSpace(r.Prompt + "\n\n" + extra)
		}
	})
	log.Printf("candyland: run %s started", id)
	go ex.Execute(c, id, ctrl)
}

// newControl makes a fresh executor command channel. Each executor generation
// gets its own (set under rt.mu) so commands can never cross between a
// terminated executor and its replacement. Buffered so senders never block.
func newControl() chan string { return make(chan string, 8) }

// signal delivers a command to the run's CURRENT executor, reading rt.control
// under rt.mu (it's swapped per generation). Returns false if the buffer is full
// (no live executor draining it) — a stop/restart to a finished run.
func (c *Conductor) signal(rt *runtime, cmd string) bool {
	rt.mu.Lock()
	ch := rt.control
	rt.mu.Unlock()
	select {
	case ch <- cmd:
		return true
	default:
		return false
	}
}

// formatAnswers renders planning answers as a readable addendum to the prompt.
func formatAnswers(answers map[string]any) string {
	if len(answers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(answers))
	for k, v := range answers {
		parts = append(parts, fmt.Sprintf("- %s: %v", k, v))
	}
	return "Planning answers:\n" + strings.Join(parts, "\n")
}

// Command forwards stop|restart to the run's executor.
func (c *Conductor) Command(id, cmd string) bool {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt == nil {
		return false
	}
	return c.signal(rt, cmd)
}

// Cancel abandons a run from ANY state. Unlike stop (which pauses a running
// executor so it can be restarted), cancel is terminal: it works even while the
// run is still in the planning Q&A — before an executor exists — which is the one
// state stop can't reach. It stops any running process tree, drops the run from
// active tracking, and marks it "cancelled" — KEEPING it in ooo so it stays in
// the Tasks history (the dashboard hides terminal runs via archive, not delete).
// The cancelled flag stops a late executor update from overwriting that state.
// Returns false only for an unknown run.
func (c *Conductor) Cancel(id string) bool {
	c.mu.Lock()
	rt := c.runs[id]
	if rt == nil {
		c.mu.Unlock()
		return false
	}
	delete(c.runs, id)
	c.mu.Unlock()

	// Stop any running executor's process tree. Non-blocking: a no-op while the
	// run is still planning (no goroutine is listening on control yet).
	c.signal(rt, "stop")

	// Publish the terminal "cancelled" state under rt.mu (serialized with any
	// in-flight executor Update), then set cancelled so nothing overwrites it.
	rt.mu.Lock()
	rt.r.Status = "cancelled"
	rt.cancelled = true
	c.publish(rt.r)
	rt.mu.Unlock()
	log.Printf("candyland: run %s cancelled", id)
	return true
}

// Restart re-runs a run from a clean slate. While the executor goroutine is still
// alive (running/paused), it signals that loop to re-run. For a FINISHED run
// (done or failed — the goroutine has already returned), it clears the error and
// previous result and launches a fresh executor. A cancelled run was dropped from
// tracking and can't be restarted (start a new run instead). Returns false for an
// unknown or cancelled run.
func (c *Conductor) Restart(id string) bool {
	rt := c.tracked(id) // rehydrate from ooo if the backend restarted since the run ran
	if rt == nil {
		return false
	}
	rt.mu.Lock()
	cancelled := rt.cancelled
	alive := rt.r.Status == "running" || rt.r.Status == "paused"
	rt.mu.Unlock()
	if cancelled {
		return false
	}

	if alive {
		// The executor loop is still running — let it re-run (it clears the error
		// and re-runs fanOut with a fresh context).
		return c.signal(rt, "restart")
	}

	// Finished run: the previous executor goroutine has returned. Reset the run to
	// a clean running state (clearing the error so the re-run can reach completion)
	// and launch a fresh executor on a FRESH control channel (so any stale command
	// from the prior generation can't reach it).
	ctrl := newControl()
	rt.mu.Lock()
	rt.control = ctrl
	rt.mu.Unlock()
	c.Update(id, func(r *run.Run) {
		r.Status = "running"
		r.Error = ""
		r.PrURL = ""
		r.Phase = 0
		r.Progress = 0
		r.HasDag = false
		// Empty (non-nil) slices marshal to [] — the UI treats agents/tasks as
		// arrays (.map/.length); nil would marshal to null and crash it.
		r.Agents = []run.Agent{}
		r.Tasks = []run.Task{}
	})
	log.Printf("candyland: run %s restarted", id)
	go (&ClaudeExecutor{}).Execute(c, id, ctrl)
	return true
}

// Edit changes a run's task (mode/folders/prompt/title) in place and resets it
// to planning — clearing the previous result and INVALIDATING the cached planning
// questions so they regenerate from the new prompt. The run keeps its id (and its
// row in the Tasks history); the UI's planning flow then re-asks the (new)
// questions and Begin re-runs it. Works on a finished (done/failed) or a stopped
// (paused) run; a paused run's parked executor is terminated first so a re-plan +
// Begin can't leave two executors on the control channel. Refused for an actively
// running run (stop it first) or a cancelled/unknown run.
func (c *Conductor) Edit(id string, spec run.Spec) bool {
	rt := c.tracked(id) // rehydrate from ooo if the backend restarted since the run ran
	if rt == nil {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cancelled || rt.r.Status == "running" {
		return false // cancelled is gone; an actively running build must be stopped first
	}
	if rt.r.Status == "paused" {
		// Terminate the parked executor (it's waiting on control, so "quit" reaches
		// it directly and it exits) before we re-plan, so Begin spawns a single
		// fresh executor rather than racing the old one.
		select {
		case rt.control <- "quit":
		default:
		}
	}
	rt.r.Mode = spec.Mode
	rt.r.Folders = spec.Folders
	rt.r.Prompt = spec.Prompt
	rt.r.Title = spec.Title
	rt.r.Branch = runBranch(spec, id)
	rt.r.Status = "planning"
	rt.r.Error = ""
	rt.r.PrURL = ""
	rt.r.Phase = 0
	rt.r.Progress = 0
	rt.r.HasDag = false
	// Empty (non-nil) slices marshal to [] — the UI treats agents/tasks as arrays;
	// nil would marshal to null and crash the planning view's .map/.length.
	rt.r.Agents = []run.Agent{}
	rt.r.Tasks = []run.Task{}
	// Regenerate questions from the new prompt next time they're requested.
	rt.questions = nil
	rt.questionsDone = false
	recompute(&rt.r)
	c.publish(rt.r)
	log.Printf("candyland: run %s edited — re-planning", id)
	return true
}

// Archive marks a run as cleared from the dashboard. It stays in ooo (and so in
// the Tasks history) — archive hides, it never deletes.
//
// A run still tracked in memory (possibly with a live executor publishing it) has
// its flag set and is re-published UNDER rt.mu, exactly like Update/Cancel — so a
// concurrent executor publish can't clobber Archived back to false. A run no
// longer tracked (a terminal/old run) is updated with a storage read-modify-write,
// where there's no executor to race.
func (c *Conductor) Archive(id string) bool {
	c.mu.Lock()
	rt := c.runs[id]
	c.mu.Unlock()
	if rt != nil {
		rt.mu.Lock()
		rt.r.Archived = true
		c.publish(rt.r)
		rt.mu.Unlock()
		return true
	}

	if c.server == nil {
		return false
	}
	obj, err := c.server.Storage.Get("runs/" + id)
	if err != nil {
		return false
	}
	var r run.Run
	if err := json.Unmarshal(obj.Data, &r); err != nil {
		return false
	}
	r.Archived = true
	b, err := json.Marshal(r)
	if err != nil {
		return false
	}
	if _, err := c.server.Storage.Set("runs/"+id, json.RawMessage(b)); err != nil {
		log.Printf("candyland: archive run %s: %v", id, err)
		return false
	}
	return true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// runBranch derives a run's git branch from its spec + id. The id suffix keeps
// two runs from the same prompt from colliding on the branch (and so on the
// push / PR head). Create and Edit must derive it identically — keep this the
// single definition of the format.
func runBranch(spec run.Spec, id string) string {
	return "feat/" + slug(firstNonEmpty(spec.Title, spec.Prompt, "run")) + "-" + id
}
