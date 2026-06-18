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
// platform, dependency state, the executor that will run, and concrete
// recommendations the user can act on.
type SystemInfo struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`       // linux | windows | darwin
	Platform        string   `json:"platform"` // Linux | Windows | macOS | WSL
	Arch            string   `json:"arch"`
	Executor        string   `json:"executor"`  // claude | scripted
	Simulated       bool     `json:"simulated"` // true when no real claude
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

	executor := "scripted"
	switch os.Getenv("CANDYLAND_EXECUTOR") {
	case "scripted":
		executor = "scripted"
	case "claude":
		executor = "claude"
	default:
		if claudeOK {
			executor = "claude"
		}
	}
	simulated := executor != "claude"

	deps := []Dep{
		{Name: "claude", Installed: claudeOK, Version: claudeVer, Path: claudePath, Install: claudeInstall(osName), Why: "runs real agents (headless Claude Code); without it runs are simulated"},
		{Name: "git", Installed: gitOK, Version: gitVer, Path: gitPath, Install: gitInstall(osName), Why: "agents work in git worktrees; the PR is opened from git"},
	}

	recs := []string{}
	if !claudeOK {
		recs = append(recs, "Claude Code isn't installed — runs use a simulated executor. Install it to run real agents: "+claudeInstall(osName))
	} else {
		recs = append(recs, "Tip: if a real run errors immediately, make sure Claude Code is authenticated (`claude` once interactively, or set ANTHROPIC_API_KEY).")
	}
	if !gitOK {
		recs = append(recs, "git isn't installed — install it so agents can use worktrees and open PRs: "+gitInstall(osName))
	}

	return SystemInfo{
		Version:         version.Version,
		OS:              osName,
		Platform:        platformLabel(wsl),
		Arch:            runtime.GOARCH,
		Executor:        executor,
		Simulated:       simulated,
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
