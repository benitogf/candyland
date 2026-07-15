//go:build !webview

package main

import "github.com/benitogf/ooo"

// runUI (default build) keeps candyland a headless server with no CGO/webview
// dependency: it serves the SPA on spaPort and blocks until shutdown. When
// --openBrowser is set it still opens the dashboard in the OS default browser so
// a visible surface materializes even without a webview build, recording mode
// "browser" on success. This is the cross-compiled single-binary the sidecar and
// CI use; build with -tags webview for the desktop window.
func runUI(server *ooo.Server, spaURL string, _ bool, _, _ int, _, openBrowser bool, setMode func(string)) {
	if openBrowser && openDashboard(spaURL) {
		setMode("browser")
	}
	server.WaitClose()
}
