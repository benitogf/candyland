package httpapi

import (
	"strings"
	"testing"

	"github.com/benitogf/ooo/key"
)

func TestSlugify(t *testing.T) {
	// Alphanumeric-only: a workspace id is an ooo storage key, which forbids '-'
	// and other separators (see slugify's doc + TestSlugifyKeysAreOooValid).
	cases := map[string]string{
		"Full Stack!":      "fullstack",
		"  Hello  World  ": "helloworld",
		"web":              "web",
		"Reports API":      "reportsapi",
		"---":              "",
		"":                 "",
		"UPPER_snake.dot":  "uppersnakedot",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	// Long input is capped (matches the ooo key length the server stores under).
	if got := slugify(strings.Repeat("a", 100)); len(got) > 32 {
		t.Errorf("slugify did not cap length: got %d chars", len(got))
	}
}

// TestSlugifyKeysAreOooValid guards the workspace-creation bug: a label with
// spaces/punctuation must still produce an id that is a VALID ooo storage key
// (ooo rejects '-' and friends, so a hyphenated id silently fails to persist).
func TestSlugifyKeysAreOooValid(t *testing.T) {
	for _, label := range []string{"Reports API", "Full Stack!", "My Web App", "a-b-c", "x.y.z", "Web"} {
		id := slugify(label)
		if id == "" {
			t.Errorf("slugify(%q) produced an empty id", label)
			continue
		}
		if k := "workspaces/" + id; !key.IsValid(k) {
			t.Errorf("slugify(%q) = %q → key %q is not a valid ooo key", label, id, k)
		}
	}
}

func TestBuildSystemInfo(t *testing.T) {
	s := buildSystemInfo()

	// The detected platform is one of the known labels, and arch is reported.
	switch s.Platform {
	case "Linux", "WSL", "macOS", "Windows":
	default:
		t.Errorf("unexpected platform label %q", s.Platform)
	}
	if s.Arch == "" {
		t.Error("arch not reported")
	}

	// The three real dependencies are always reported, each with an install
	// command for the platform (so the Setup panel can guide a missing one). There
	// is no demo/simulated executor concept anymore — runs are always real claude.
	byName := map[string]Dep{}
	for _, d := range s.Deps {
		byName[d.Name] = d
	}
	for _, name := range []string{"claude", "git", "gh"} {
		d, ok := byName[name]
		if !ok {
			t.Errorf("dependency %q not reported", name)
			continue
		}
		if d.Install == "" {
			t.Errorf("dependency %q has no install command", name)
		}
		if d.Why == "" {
			t.Errorf("dependency %q has no rationale", name)
		}
	}
}
