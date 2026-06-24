package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/benitogf/candyland/internal/version"
	"github.com/benitogf/ooo"
)

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
	out, _ := exec.CommandContext(ctx, bin, "--version").Output()
	return path, strings.TrimSpace(string(out)), true
}

func buildSystemInfo() SystemInfo {
	wsl := detectWSL()
	osName := runtime.GOOS

	claudePath, claudeVer, claudeOK := depVersion("claude")
	gitPath, gitVer, gitOK := depVersion("git")
	ghPath, ghVer, ghOK := depVersion("gh")

	deps := []Dep{
		{Name: "claude", Installed: claudeOK, Version: claudeVer, Path: claudePath, Install: claudeInstall(osName), Why: "drives the agents (headless Claude Code); runs need it — there is no demo mode"},
		{Name: "git", Installed: gitOK, Version: gitVer, Path: gitPath, Install: gitInstall(osName), Why: "agents work in git worktrees; the run branch is committed and pushed from git"},
		{Name: "gh", Installed: ghOK, Version: ghVer, Path: ghPath, Install: ghInstall(osName), Why: "opens the pull request the run delivers"},
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
