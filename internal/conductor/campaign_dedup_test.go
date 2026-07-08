package conductor

import "testing"

// === Cross-quest partition dedup =============================================
//
// dedupOverlappingQuests collapses two quests that pursue the SAME objective over
// OVERLAPPING folders down to one, so the campaign never spawns two runs against the
// same scope on the shared branch. It keeps the first quest of each overlapping group,
// drops the rest, and repoints any dependency on a dropped quest onto the kept one.
// Folder scopes are compared AFTER resolution through resolveQuestFolders (token →
// campaign folder), matching the runtime, not the tech manager's raw tokens.

func ids(quests []questPartitionItem) []string {
	out := make([]string, len(quests))
	for i, q := range quests {
		out[i] = q.ID
	}
	return out
}

func TestDedupDropsSameObjectiveOverlappingFolders(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"pkg/client", "pkg/util"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry to the client", Folders: []string{"pkg/client"}},
		{ID: "b", Objective: "add   retry  to the CLIENT", Folders: []string{"pkg/client", "pkg/util"}},
	})
	if got := ids(out); len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected only quest a to survive, got %v", got)
	}
}

func TestDedupKeepsDistinctObjectives(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"pkg/client"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry to the client", Folders: []string{"pkg/client"}},
		{ID: "b", Objective: "Document the client", Folders: []string{"pkg/client"}},
	})
	if got := ids(out); len(got) != 2 {
		t.Fatalf("distinct objectives must both survive, got %v", got)
	}
}

func TestDedupKeepsSameObjectiveDisjointFolders(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"/w/client", "/w/server"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: []string{"client"}},
		{ID: "b", Objective: "Add retry", Folders: []string{"server"}},
	})
	if got := ids(out); len(got) != 2 {
		t.Fatalf("disjoint folders must both survive, got %v", got)
	}
}

func TestDedupEmptyFolderScopeOverlapsAnySameObjective(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"pkg/client"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: nil},
		{ID: "b", Objective: "Add retry", Folders: []string{"pkg/client"}},
	})
	if got := ids(out); len(got) != 1 || got[0] != "a" {
		t.Fatalf("empty scope inherits campaign folders and must subsume, got %v", got)
	}
}

func TestDedupRepointsDependencyOntoKeptQuest(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"pkg/client", "cmd"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: []string{"pkg/client"}},
		{ID: "b", Objective: "add retry", Folders: []string{"pkg/client"}},
		{ID: "c", Objective: "Wire it up", Folders: []string{"cmd"}, Deps: []string{"b"}},
	})
	if got := ids(out); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("expected a and c to survive, got %v", got)
	}
	var c questPartitionItem
	for _, q := range out {
		if q.ID == "c" {
			c = q
		}
	}
	if len(c.Deps) != 1 || c.Deps[0] != "a" {
		t.Fatalf("dep on dropped b must repoint to a, got %v", c.Deps)
	}
}

func TestDedupExactIDDuplicateCollapses(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"pkg/client"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: []string{"pkg/client"}},
		{ID: "a", Objective: "Add retry", Folders: []string{"pkg/client"}},
	})
	if got := ids(out); len(got) != 1 {
		t.Fatalf("exact duplicate must collapse, got %v", got)
	}
}

func TestDedupCollapsesBasenameVsFullPathTokens(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"/w/candyland"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: []string{"candyland"}},
		{ID: "b", Objective: "add retry", Folders: []string{"/w/candyland"}},
	})
	if got := ids(out); len(got) != 1 || got[0] != "a" {
		t.Fatalf("basename vs full-path tokens resolve to the same folder — must collapse, got %v", got)
	}
}

func TestDedupCollapsesUnmatchedTokensSingleRepoCampaign(t *testing.T) {
	out := dedupOverlappingQuests(nil, "", []string{"/w/candyland"}, []questPartitionItem{
		{ID: "a", Objective: "Add retry", Folders: []string{"pkg/client"}},
		{ID: "b", Objective: "add retry", Folders: []string{"pkg/server"}},
	})
	if got := ids(out); len(got) != 1 || got[0] != "a" {
		t.Fatalf("unmatched tokens both resolve to ALL campaign folders in a single-repo campaign — must collapse, got %v", got)
	}
}
