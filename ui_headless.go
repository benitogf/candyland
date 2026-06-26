//go:build !webview

package main

import "github.com/benitogf/ooo"

// runUI (default build) keeps candyland a headless server: it serves the SPA on
// spaPort and blocks until shutdown, with no desktop window and no CGO/webview
// dependency. This is the cross-compiled single-binary the sidecar and CI use;
// build with -tags webview for the desktop window.
func runUI(server *ooo.Server, _ string, _ bool, _, _ int, _ bool) {
	server.WaitClose()
}
