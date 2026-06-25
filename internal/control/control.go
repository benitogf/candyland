// Package control is the trigger surface a VSCode Claude Code session reaches
// through its MCP config (`candyland control-mcp`) to launch and stop candyland
// runs. It is a thin HTTP client to the already-running candyland sidecar's API:
// launch_run starts the SAME multi-agent flow the conductor has always run (a
// tech-lead coordinating coders over the ooo bus), the session then monitors in
// the candyland UI, and stop_run is the escape hatch for a hung/wrong run.
//
// The entry point moves from the candyland web wizard into the editor session;
// candyland keeps spawning the agents and tracking the run's tasks. The session
// triggers once and gets out of the way (least tokens) — it does not poll.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/benitogf/candyland/internal/run"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client calls a running candyland sidecar's REST API (the same endpoints the
// web UI uses). base is the api origin, e.g. http://127.0.0.1:8888.
type Client struct {
	base string
	http *http.Client
	mu   sync.Mutex // serializes ensureUp so concurrent launches don't double-spawn
}

// NewClient builds a control client for the sidecar at addr (host:port or a full
// origin); a bare host:port is assumed http. A bare host with no port defaults to
// candyland's :8888, so ping() and the spawned server agree on where to bind and
// reach (otherwise a portless addr pings :80 while the server binds its default).
func NewClient(addr string) *Client {
	addr = strings.TrimRight(addr, "/")
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	if u, err := url.Parse(addr); err == nil && u.Port() == "" {
		u.Host = u.Hostname() + ":8888"
		addr = u.String()
	}
	return &Client{base: addr, http: &http.Client{}}
}

// ping reports whether the sidecar is up and answering at base. Short timeout so
// the "is it running?" check is snappy.
func (c *Client) ping() bool {
	cl := &http.Client{Timeout: 1500 * time.Millisecond}
	res, err := cl.Get(c.base + "/api/system")
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// ensureUp guarantees the sidecar is running before we delegate to it: it
// health-checks first, and only if that fails does it start the sidecar
// (detached, so it outlives this control-mcp tool call), then HANDSHAKES —
// polling /api/system until the server actually answers — before returning. If
// the sidecar never comes up it returns an honest error rather than letting the
// caller fire a request into a dead port. This is the candyland binary itself in
// server mode (no subcommand), bound to the host:port the control client targets.
func (c *Client) ensureUp() error {
	c.mu.Lock() // serialize: concurrent launches must not each spawn a sidecar
	defer c.mu.Unlock()
	if c.ping() {
		return nil // already running — reuse the persistent local sidecar
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate candyland binary: %w", err)
	}
	host, port := hostPort(c.base)
	args := []string{}
	if host != "" {
		args = append(args, "--host", host)
	}
	if port != "" {
		args = append(args, "--port", port)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil // a daemon, not attached to this process's I/O
	detachSysProc(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start candyland sidecar: %w", err)
	}
	_ = cmd.Process.Release() // hand it off — it runs independently of control-mcp

	// Handshake: wait for the server to bind + answer before delegating.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if c.ping() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("candyland sidecar did not become ready at %s within 20s (is another process using that port?)", c.base)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// hostPort splits c.base (an http origin) into host + port for spawning the
// server bound where the client expects it.
func hostPort(base string) (string, string) {
	u, err := url.Parse(base)
	if err != nil {
		return "", ""
	}
	return u.Hostname(), u.Port()
}

// Launch ensures the sidecar is up (starting + handshaking it if needed), then
// creates a run from the supplied folders + prompt and begins it immediately. It
// skips the web-UI planning Q&A: the plan is settled upstream in the editor
// session (/plan + truthseeker), so the run goes straight to build. Returns the
// run id.
func (c *Client) Launch(spec run.Spec) (string, error) {
	if err := c.ensureUp(); err != nil {
		return "", err
	}
	body, _ := json.Marshal(spec)
	res, err := c.http.Post(c.base+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create run: %s", httpErr(res))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("create run: empty id")
	}
	if err := c.post("/api/runs/"+out.ID+"/begin", nil); err != nil {
		return out.ID, fmt.Errorf("begin run: %w", err)
	}
	return out.ID, nil
}

// Status reads a run's current snapshot (the same object the UI renders).
func (c *Client) Status(id string) (run.Run, error) {
	var r run.Run
	res, err := c.http.Get(c.base + "/api/runs/" + id)
	if err != nil {
		return r, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return r, fmt.Errorf("status: %s", httpErr(res))
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return r, err
	}
	return r, nil
}

// Stop halts a run — the escape hatch for a hung or wrong run. candyland spawned
// the run's processes, so this genuinely kills its tech-lead + coder tree.
func (c *Client) Stop(id string) error {
	return c.post("/api/runs/"+id+"/command", map[string]string{"command": "stop"})
}

func (c *Client) post(path string, body any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	res, err := c.http.Post(c.base+path, "application/json", rdr)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s", httpErr(res))
	}
	return nil
}

func httpErr(res *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return res.Status + ": " + msg
	}
	return res.Status
}

// RegisterTools registers the trigger surface on an MCP server: launch_run /
// run_status / stop_run. Lean by design — candyland is observe + audit + a stop
// escape hatch, not a remote control.
func RegisterTools(server *mcp.Server, c *Client) {
	type launchArgs struct {
		Prompt  string   `json:"prompt" jsonschema:"What to build — the settled plan/instruction for the multi-agent run (e.g. the contents of the .plan contract from /plan)."`
		Folders []string `json:"folders,omitempty" jsonschema:"Working folders for the run; folders[0] is the git repo it branches and opens its PR in. Defaults to the current working directory when omitted."`
		Mode    string   `json:"mode,omitempty" jsonschema:"developer (default) or non-developer."`
		Title   string   `json:"title,omitempty" jsonschema:"Optional short label for the run."`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "launch_run",
		Description: "Launch a candyland multi-agent run (a tech-lead coordinating coders over the ooo bus) and begin building immediately. Monitor and audit it in the candyland dashboard; use stop_run to halt it. Run /plan first to settle the work, then pass that plan as the prompt.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a launchArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Prompt) == "" {
			return errResult("launch_run: a prompt is required (the settled plan/instruction)"), nil, nil
		}
		folders := a.Folders
		if len(folders) == 0 {
			// Default to the editor session's cwd — control-mcp runs as that
			// session's child, so its working directory IS the session's.
			if wd, err := os.Getwd(); err == nil {
				folders = []string{wd}
			}
		}
		if len(folders) == 0 {
			return errResult("launch_run: no folders and the working directory is unavailable; pass folders explicitly (folders[0] = the git repo)"), nil, nil
		}
		mode := a.Mode
		if mode == "" {
			mode = "developer"
		}
		id, err := c.Launch(run.Spec{Mode: mode, Folders: folders, Prompt: a.Prompt, Title: a.Title})
		if err != nil {
			return errResult("launch_run: " + err.Error()), nil, nil
		}
		return textResult(fmt.Sprintf("launched run %s in %s — monitor it in the candyland dashboard; stop_run %s to halt it", id, folders[0], id)), nil, nil
	})

	type idArgs struct {
		ID string `json:"id" jsonschema:"The run id returned by launch_run."`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_status",
		Description: "Report a candyland run's current status (phase, tasks green/total, error, PR url). Call on demand — candyland observes the run live in its dashboard, so there is no need to poll.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.ID) == "" {
			return errResult("run_status: an id is required"), nil, nil
		}
		r, err := c.Status(a.ID)
		if err != nil {
			return errResult("run_status: " + err.Error()), nil, nil
		}
		line := fmt.Sprintf("run %s: %s (phase %d, tasks %d/%d green)", r.ID, statusOf(r), r.Phase, r.TasksGreen, r.TasksTotal)
		if r.Error != "" {
			line += " — error: " + r.Error
		}
		if r.PrURL != "" {
			line += " — PR: " + r.PrURL
		}
		return textResult(line), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_run",
		Description: "Stop a candyland run — the escape hatch for a hung or wrong run. Kills the run's tech-lead + coder processes (candyland owns them).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a idArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.ID) == "" {
			return errResult("stop_run: an id is required"), nil, nil
		}
		if err := c.Stop(a.ID); err != nil {
			return errResult("stop_run: " + err.Error()), nil, nil
		}
		return textResult("stopped run " + a.ID), nil, nil
	})
}

func statusOf(r run.Run) string {
	if r.Status == "" {
		return "unknown"
	}
	return r.Status
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: true}
}
