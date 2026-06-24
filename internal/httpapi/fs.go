package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benitogf/ooo"
)

// Filesystem endpoints back the workspace folder picker and the realtime
// "is this folder still there / usable?" check. They expose the backend's own
// filesystem (the process that will run the agents), so a path the UI shows as
// valid is genuinely reachable + writable by the thing that uses it. They browse
// arbitrary paths by design (you pick where your repos live), so the server binds
// to loopback by default (main.go --host) — exposing it on the network is an
// explicit opt-in, the same gate that protects the run-control endpoints.

type fsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type fsList struct {
	Path    string    `json:"path"`    // resolved absolute path being listed
	Parent  string    `json:"parent"`  // parent dir ("" when at the root)
	Entries []fsEntry `json:"entries"` // sub-directories only (workspaces are folders)
}

// fsStatus is the truth about a path on the backend's filesystem right now.
type fsStatus struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Dir      bool   `json:"dir"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

// expandPath resolves "" → home, a leading ~ → home, then makes it absolute, so
// every path the UI deals with is unambiguous (answers "where does ~ land?").
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// checkPath reports whether path is a directory the backend can read AND write.
// Writability is probed for real (create + remove a temp entry) rather than
// inferred from mode bits, so it reflects what the agent process will actually be
// able to do.
func checkPath(path string) fsStatus {
	st := fsStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		return st
	}
	st.Exists = true
	st.Dir = info.IsDir()
	if !st.Dir {
		return st
	}
	if f, err := os.Open(path); err == nil {
		_ = f.Close()
		st.Readable = true
	}
	if tmp, err := os.CreateTemp(path, ".candyland-wcheck-*"); err == nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		st.Writable = true
	}
	return st
}

// listDir returns the sub-directories of path (default: the backend's home),
// hiding dotfolders to keep the picker focused on project directories.
func listDir(path string) (fsList, error) {
	abs := expandPath(path)
	entries, err := os.ReadDir(abs)
	if err != nil {
		return fsList{}, err
	}
	out := fsList{Path: abs}
	if parent := filepath.Dir(abs); parent != abs {
		out.Parent = parent
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out.Entries = append(out.Entries, fsEntry{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	return out, nil
}

// registerFS mounts the filesystem browse + check endpoints.
func registerFS(server *ooo.Server) {
	get := ooo.Methods{"GET": ooo.MethodSpec{}}

	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/fs",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			list, err := listDir(r.URL.Query().Get("path"))
			if err != nil {
				http.Error(w, "can't read that folder: "+err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, list)
		},
	})

	server.Endpoint(ooo.EndpointConfig{
		Path:    "/api/fs/check",
		Methods: get,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Query().Get("path")
			if strings.TrimSpace(p) == "" {
				http.Error(w, "path required", http.StatusBadRequest)
				return
			}
			writeJSON(w, checkPath(expandPath(p)))
		},
	})
}
