// Package spa serves the embedded built React app with client-side-routing
// fallback (any unknown path returns index.html so /run/:id deep links work).
package spa

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// Handler serves files from fsys, falling back to index.html for routes that
// don't map to a real asset (so the SPA router handles them).
func Handler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := fsys.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path → serve index.html for client-side routing.
		index, err := fsys.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer index.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, index)
	})
}
