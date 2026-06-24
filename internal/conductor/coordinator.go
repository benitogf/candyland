package conductor

import (
	"github.com/benitogf/candyland/internal/bus"
	"github.com/benitogf/ooo"
)

// OrchestratorID is the single-writer identity for the task-graph ledger. The
// conductor (pure Go, zero model tokens) is the orchestrator; the tech-lead and
// coders reach the bus as workers (they propose; the conductor commits).
const OrchestratorID = "conductor"

// StartBus registers the coordination bus (Realization B) on the conductor's
// ooo server — the task-graph + cursor filters and the re-plan reactor — and
// returns it so the conductor can register each agent's inbox at spawn. Must be
// called before server.Start (filters register before the listener binds). The
// bus is a back-channel beside the stdout loop, which is untouched.
func StartBus(server *ooo.Server) *bus.Bus {
	b := bus.NewBus(OrchestratorID, bus.CursorReader(server))
	b.RegisterGlobal(server)
	b.RegisterReactor(server, func(srv *ooo.Server, ev bus.Envelope) {
		// Re-plan: acknowledge the worker's proposal with a directive it consumes
		// next turn, then auto-unblock any nodes whose deps are now done.
		_ = b.PushDirective(srv, ev.From, "noted: "+ev.Body)
		b.AutoUnblock(srv)
	})
	return b
}
