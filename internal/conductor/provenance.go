package conductor

import "strings"

// provenanceFooter builds the trailing attribution line shared by every candyland
// PR body. It names the delivery vehicle (kind) and, when known, the originating
// entity id so a merged PR is traceable back to the run/quest/campaign that opened
// it. kind defaults to "run"; a blank id simply omits the id clause.
func provenanceFooter(kind, id string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "run"
	}
	footer := "🍬 Opened by [candyland](https://github.com/benitogf/candyland) — " + kind
	if id = strings.TrimSpace(id); id != "" {
		footer += " `" + id + "`"
	}
	return footer + "."
}
