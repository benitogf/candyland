package httpapi

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Full Stack!":      "full-stack",
		"  Hello  World  ": "hello-world",
		"web":              "web",
		"Reports API":      "reports-api",
		"---":              "",
		"":                 "",
		"UPPER_snake.dot":  "upper-snake-dot",
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

func TestQuestionsFor(t *testing.T) {
	for _, mode := range []string{"developer", "non-developer"} {
		if len(questionsFor(mode)) == 0 {
			t.Errorf("questionsFor(%q) returned no questions", mode)
		}
	}
	// Unknown / empty mode falls back to the non-developer set (never empty/nil).
	fallback := questionsFor("non-developer")
	for _, mode := range []string{"", "bogus", "Developer"} {
		got := questionsFor(mode)
		if len(got) != len(fallback) {
			t.Errorf("questionsFor(%q) = %d questions, want fallback of %d", mode, len(got), len(fallback))
		}
	}
}

func TestBuildSystemInfoExecutorModes(t *testing.T) {
	t.Setenv("CANDYLAND_EXECUTOR", "scripted")
	if s := buildSystemInfo(); s.Executor != "scripted" || !s.Simulated {
		t.Errorf("forced scripted: executor=%q simulated=%v, want scripted/true", s.Executor, s.Simulated)
	}

	t.Setenv("CANDYLAND_EXECUTOR", "claude")
	s := buildSystemInfo()
	if s.Executor != "claude" || s.Simulated {
		t.Errorf("forced claude: executor=%q simulated=%v, want claude/false", s.Executor, s.Simulated)
	}
	// executor/simulated must always stay coherent.
	if (s.Executor == "claude") == s.Simulated {
		t.Errorf("executor/simulated incoherent: executor=%q simulated=%v", s.Executor, s.Simulated)
	}
	// The detected platform is one of the known labels, arch is reported, and the
	// claude dep always carries an install command for the platform.
	switch s.Platform {
	case "Linux", "WSL", "macOS", "Windows":
	default:
		t.Errorf("unexpected platform label %q", s.Platform)
	}
	if s.Arch == "" {
		t.Error("arch not reported")
	}
	var claude *Dep
	for i := range s.Deps {
		if s.Deps[i].Name == "claude" {
			claude = &s.Deps[i]
		}
	}
	if claude == nil || claude.Install == "" {
		t.Errorf("claude dep missing or has no install command: %+v", claude)
	}
}
