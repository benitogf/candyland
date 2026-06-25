package httpapi

import (
	"testing"
)

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
