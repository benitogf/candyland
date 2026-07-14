package conductor

import (
	"fmt"
	"regexp"
	"strings"
)

// provenanceFooter builds the trailing attribution line shared by every candyland
// PR body. It names the delivery vehicle (kind) and, when known, the originating
// entity id so a merged PR is traceable back to the run/quest that opened
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

// closingKeywordRe matches GitHub's nine recognized issue-closing keywords
// followed by a same-repo `#N` reference (the `[:\s]+` accepts both `Closes #42`
// and the conventional-commits `Closes: #42` form). Cross-repo `owner/repo#N`
// forms are intentionally NOT matched — a candyland delivery closes issues in the
// repo it opens the PR against, and re-emitting a cross-repo close is riskier than
// the value it adds.
var closingKeywordRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[:\s]+#(\d+)`)

// closingTrailer returns a normalized, GitHub-parseable close trailer for every
// closing reference found in source, or "" when there is none. The stamped
// request/objective text is authored freely (a plan contract, a quest objective),
// so a `Closes #N` in it may ride in wrapped in backticks or another code span —
// where GitHub's own auto-close parser ignores it. Re-emitting each referenced
// issue as a bare `Closes #N` line guarantees the merge auto-closes the issues the
// delivery was meant to close, regardless of how the source text formatted them.
// Numbers are de-duplicated in first-seen order; every keyword variant normalizes
// to `Closes`, since all nine forms close identically.
func closingTrailer(source string) string {
	seen := map[string]bool{}
	var lines []string
	for _, m := range closingKeywordRe.FindAllStringSubmatch(source, -1) {
		n := m[1]
		if seen[n] {
			continue
		}
		seen[n] = true
		lines = append(lines, fmt.Sprintf("Closes #%s", n))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(lines, "\n")
}
