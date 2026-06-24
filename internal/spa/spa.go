// Package spa serves the embedded built React app with client-side-routing
// fallback (any unknown path returns index.html so /run/:id deep links work).
package spa

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// Handler serves files from fsys, falling back to index.html for routes that
// don't map to a real asset (so the SPA router handles them). The API port is
// injected into index.html as window.__CANDYLAND_API_PORT__, so the client talks
// to the backend on the binary's actual --port instead of a hardcoded one.
func Handler(fsys fs.FS, apiPort int) http.Handler {
	index := renderIndex(fsys, apiPort)
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		// index.html (root or explicit) and any client-side route get the
		// port-injected document; real asset paths are served as-is.
		if p == "" || p == "index.html" {
			serveIndex(w, index)
			return
		}
		if f, err := fsys.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, index) // unknown path → SPA router handles it
	})
}

// renderIndex reads index.html once and injects the API-port script into <head>.
func renderIndex(fsys fs.FS, apiPort int) []byte {
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil
	}
	inject := []byte(fmt.Sprintf("<script>window.__CANDYLAND_API_PORT__=%d;</script>", apiPort))
	if i := bytes.Index(b, []byte("</head>")); i >= 0 {
		out := make([]byte, 0, len(b)+len(inject))
		out = append(out, b[:i]...)
		out = append(out, inject...)
		out = append(out, b[i:]...)
		return out
	}
	return append(inject, b...)
}

func serveIndex(w http.ResponseWriter, index []byte) {
	if index == nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}
