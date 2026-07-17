package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/conductor"
	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/candyland/internal/winproc"
	"github.com/benitogf/ooo"
)

// GhCapability reports whether the resolved gh CLI can actually DELIVER a run —
// installed, authenticated, and holding the OAuth scopes a push may need. It is
// probed at launch (the gate) and surfaced standing in the system panel, so the
// build-30-minutes-then-fail-at-push class is caught before a run starts. It is
// internal plumbing (the panel surfaces through Dep.Why + Recommendations, the
// gate through ghGateReject); it is never marshaled, so it carries no json tags.
type GhCapability struct {
	Installed   bool
	Authed      bool
	Scopes      []string
	ScopesKnown bool
	Missing     []string // subset of {repo, workflow}
	Remedy      string   // exact command to fix
}

// ghRequiredScopes is the single place the delivery-capable scope set lives —
// extend here if a future push class needs another scope.
var ghRequiredScopes = []string{"repo", "workflow"}

// parseGhAuthStatus turns `gh auth status` output into a GhCapability. It is
// PURE (no exec) so the whole capability judgement is table-testable. gh prints
// one block per authenticated account, each with a `- Active account: true|false`
// marker and its own `- Token scopes:` line (quoted+comma-separated, e.g.
// `- Token scopes: 'gist', 'repo'`). The gate must judge the ACTIVE account's
// token — the one gh actually uses for git/PR operations — so we select the block
// marked active (falling back to the first block for older single-account output
// that has no `Active account:` line). Fine-grained PATs / GitHub App tokens print
// NO scopes line, which we fail OPEN on (ScopesKnown=false, no Missing) — blocking
// on a guess would lock out valid setups.
func parseGhAuthStatus(out string, installed bool) GhCapability {
	if !installed {
		return GhCapability{Installed: false, Remedy: ghInstall(runtime.GOOS)}
	}
	ghc := GhCapability{Installed: true}

	block := activeAccountBlock(out)
	if !strings.Contains(strings.ToLower(block), "logged in to github.com") {
		ghc.Remedy = "run `gh auth login`"
		return ghc
	}
	ghc.Authed = true

	// Find the active block's "Token scopes:" line (case-insensitive), parse tolerantly.
	for _, line := range strings.Split(block, "\n") {
		lower := strings.ToLower(line)
		idx := strings.Index(lower, "token scopes:")
		if idx < 0 {
			continue
		}
		ghc.ScopesKnown = true
		rest := line[idx+len("token scopes:"):]
		for _, tok := range strings.Split(rest, ",") {
			s := strings.Trim(strings.TrimSpace(tok), "'`\" ")
			if s != "" {
				ghc.Scopes = append(ghc.Scopes, s)
			}
		}
		break
	}
	if !ghc.ScopesKnown {
		return ghc // fine-grained token — fail open
	}
	for _, req := range ghRequiredScopes {
		found := false
		for _, s := range ghc.Scopes {
			if strings.EqualFold(s, req) {
				found = true
				break
			}
		}
		if !found {
			ghc.Missing = append(ghc.Missing, req)
		}
	}
	if len(ghc.Missing) > 0 {
		ghc.Remedy = "gh auth refresh -h github.com -s " + strings.Join(ghc.Missing, ",")
	}
	return ghc
}

// activeAccountBlock splits `gh auth status` output into per-account blocks (each
// starts at a "Logged in to" line) and returns the one marked `Active account:
// true`. With no active marker (older single-account gh) it returns the first
// block; with no "Logged in to" line at all it returns the whole output unchanged
// so the caller's not-authed detection still fires.
func activeAccountBlock(out string) string {
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(line), "logged in to") {
			flush() // start a new account block (drops any pre-login header lines)
			cur = []string{line}
			continue
		}
		if cur != nil {
			cur = append(cur, line)
		}
	}
	flush()
	if len(blocks) == 0 {
		return out
	}
	for _, b := range blocks {
		if strings.Contains(strings.ToLower(b), "active account: true") {
			return b
		}
	}
	return blocks[0]
}

// ghCapability probes the resolved gh binary. The `auth status` call is bounded
// so a hung gh can't stall a launch; gh has written this to stderr historically
// and stdout in newer versions, so both are captured before parsing.
func ghCapability() GhCapability {
	if _, err := exec.LookPath(conductor.GhBin()); err != nil {
		return parseGhAuthStatus("", false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, conductor.GhBin(), "auth", "status")
	winproc.Configure(cmd)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	return parseGhAuthStatus(buf.String(), true)
}

// Dep reports whether a required CLI is present and its version.
type Dep struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Install   string `json:"install,omitempty"` // platform-specific install command
	Why       string `json:"why"`               // what candyland needs it for
}

// SystemInfo is what the UI's setup/status panel renders — the detected
// platform, dependency state, and concrete recommendations the user can act on.
// Runs are always driven by real Claude Code; whether that's possible is read
// straight off the claude dependency's Installed flag (no demo/simulated mode).
type SystemInfo struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`       // linux | windows | darwin
	Platform        string   `json:"platform"` // Linux | Windows | macOS | WSL
	Arch            string   `json:"arch"`
	Deps            []Dep    `json:"deps"`
	Recommendations []string `json:"recommendations"`
}

// detectWSL reports whether we're running under WSL (Linux kernel reports it).
func detectWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(b))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}

func platformLabel(wsl bool) string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		if wsl {
			return "WSL"
		}
		return "Linux"
	default:
		return runtime.GOOS
	}
}

// claudeInstall returns the platform-appropriate Claude Code install command.
func claudeInstall(osName string) string {
	if osName == "windows" {
		return "irm https://claude.ai/install.ps1 | iex"
	}
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

// gitInstall returns the platform-appropriate git install command. (Linux and
// WSL share the same package-manager command, so it keys only on osName.)
func gitInstall(osName string) string {
	switch osName {
	case "windows":
		return "winget install --id Git.Git -e"
	case "darwin":
		return "brew install git" // or: xcode-select --install
	default: // linux / WSL
		return "sudo apt-get install -y git" // Debian/Ubuntu (incl. WSL); use your distro's package manager otherwise
	}
}

// ghInstall returns the platform-appropriate GitHub CLI install command.
func ghInstall(osName string) string {
	switch osName {
	case "windows":
		return "winget install --id GitHub.cli -e"
	case "darwin":
		return "brew install gh"
	default: // linux / WSL
		return "sudo apt-get install -y gh" // see https://github.com/cli/cli for other distros
	}
}

// depVersion reports a CLI's path and `--version` output if present. The probe
// is bounded so a hung/slow binary can't stall the /api/system response.
func depVersion(bin string) (string, string, bool) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	winproc.Configure(cmd) // windowless: no flashing console for the probe on Windows
	out, _ := cmd.Output()
	return path, strings.TrimSpace(string(out)), true
}

// ghGateReject is the launch gate: creating a run or quest requires a
// delivery-capable gh, so the build-then-fail-at-push class is caught before a
// run starts. It rejects when gh is absent, unauthenticated, or KNOWN to be
// missing a required scope. When scopes are uninspectable (fine-grained token)
// it fails OPEN — the enriched push error is the safety net. Returns (msg, false)
// to reject (msg is the one-line reason + remedy), or ("", true) to allow.
func ghGateReject() (msg string, ok bool) {
	ghc := ghCapability()
	if !ghc.Installed || !ghc.Authed || (ghc.ScopesKnown && len(ghc.Missing) > 0) {
		switch {
		case !ghc.Installed:
			return "gh (GitHub CLI) isn't installed — a run can't deliver its PR. " + ghc.Remedy, false
		case !ghc.Authed:
			return "gh isn't authenticated — a run can build but never deliver; " + ghc.Remedy + ".", false
		default:
			return "gh token is missing the `" + strings.Join(ghc.Missing, ", ") + "` scope(s) — delivery would fail; run `" + ghc.Remedy + "`.", false
		}
	}
	return "", true
}

func buildSystemInfo() SystemInfo {
	wsl := detectWSL()
	osName := runtime.GOOS

	claudePath, claudeVer, claudeOK := depVersion("claude")
	gitPath, gitVer, gitOK := depVersion("git")
	ghPath, ghVer, ghOK := depVersion("gh")
	ghCap := ghCapability()

	ghWhy := "opens the pull request the run delivers"
	if ghOK {
		switch {
		case !ghCap.Authed:
			ghWhy += " (not authenticated)"
		case ghCap.ScopesKnown:
			ghWhy += " (authenticated; scopes: " + strings.Join(ghCap.Scopes, ", ") + ")"
		default:
			ghWhy += " (authenticated; scopes not inspectable)"
		}
	}

	deps := []Dep{
		{Name: "claude", Installed: claudeOK, Version: claudeVer, Path: claudePath, Install: claudeInstall(osName), Why: "drives the agents (headless Claude Code); runs need it — there is no demo mode"},
		{Name: "git", Installed: gitOK, Version: gitVer, Path: gitPath, Install: gitInstall(osName), Why: "agents work in git worktrees; the run branch is committed and pushed from git"},
		{Name: "gh", Installed: ghOK, Version: ghVer, Path: ghPath, Install: ghInstall(osName), Why: ghWhy},
	}

	recs := []string{}
	if !claudeOK {
		recs = append(recs, "Claude Code isn't installed — runs need it to drive the agents. Install it: "+claudeInstall(osName))
	} else {
		recs = append(recs, "Tip: if a run errors immediately, make sure Claude Code is authenticated (`claude` once interactively, or set ANTHROPIC_API_KEY).")
	}
	if !gitOK {
		recs = append(recs, "git isn't installed — install it so agents can use worktrees and push the run branch: "+gitInstall(osName))
	}
	if !ghOK {
		recs = append(recs, "GitHub CLI (gh) isn't installed — install and authenticate it (`gh auth login`) so a run can open its PR: "+ghInstall(osName))
	} else if !ghCap.Authed {
		recs = append(recs, "gh isn't authenticated — runs can build but never deliver; run `gh auth login`.")
	} else if ghCap.ScopesKnown && len(ghCap.Missing) > 0 {
		recs = append(recs, "gh token is missing the `"+strings.Join(ghCap.Missing, ", ")+"` scope(s) — delivery will fail for any run whose diff needs them; run `"+ghCap.Remedy+"`.")
	} else if !ghCap.ScopesKnown {
		recs = append(recs, "gh token scopes aren't inspectable (fine-grained token) — the launch gate can't verify delivery capability; a push may still be rejected at delivery.")
	}

	return SystemInfo{
		Version:         version.Version,
		OS:              osName,
		Platform:        platformLabel(wsl),
		Arch:            runtime.GOARCH,
		Deps:            deps,
		Recommendations: recs,
	}
}

// registerSystem mounts GET /api/system (also the reachability probe).
func registerSystem(server *ooo.Server) {
	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/system",
		Methods: ooo.Methods{"GET": ooo.MethodSpec{}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, buildSystemInfo())
		},
	})
}
