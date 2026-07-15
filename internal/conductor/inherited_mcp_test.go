package conductor

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog redirects the standard logger to a buffer for the duration of fn,
// returning what was logged, so a warning-naming test can assert on it.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func TestInheritedMCPWarnsOnUnreadablePath(t *testing.T) {
	// A path-shaped value (not valid JSON) that does not exist.
	t.Setenv("CANDYLAND_INHERITED_MCP", filepath.Join(t.TempDir(), "does-not-exist.json"))
	var servers map[string]mcpServerSpec
	out := captureLog(t, func() { servers = inheritedMCPServers() })
	if servers == nil || len(servers) != 0 {
		t.Errorf("want empty non-nil map, got %v", servers)
	}
	if !strings.Contains(out, "unreadable") {
		t.Errorf("warning must name the unreadable-path reason, got %q", out)
	}
}

func TestInheritedMCPWarnsOnInvalidJSON(t *testing.T) {
	// A valid, existing file whose contents are not JSON — read succeeds, parse fails.
	f := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(f, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANDYLAND_INHERITED_MCP", f)
	var servers map[string]mcpServerSpec
	out := captureLog(t, func() { servers = inheritedMCPServers() })
	if len(servers) != 0 {
		t.Errorf("want empty map, got %v", servers)
	}
	if !strings.Contains(out, "not valid mcp-config JSON") {
		t.Errorf("warning must name the invalid-JSON reason, got %q", out)
	}
}

func TestInheritedMCPWarnsOnEmptyServers(t *testing.T) {
	t.Setenv("CANDYLAND_INHERITED_MCP", `{"mcpServers":{}}`)
	var servers map[string]mcpServerSpec
	out := captureLog(t, func() { servers = inheritedMCPServers() })
	if len(servers) != 0 {
		t.Errorf("want empty map, got %v", servers)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("warning must name the empty-mcpServers reason, got %q", out)
	}
}

func TestInheritedMCPUnsetIsSilent(t *testing.T) {
	t.Setenv("CANDYLAND_INHERITED_MCP", "")
	var servers map[string]mcpServerSpec
	out := captureLog(t, func() { servers = inheritedMCPServers() })
	if servers == nil {
		t.Error("want empty non-nil map")
	}
	if out != "" {
		t.Errorf("unset env must not warn, got %q", out)
	}
}
