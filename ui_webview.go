//go:build webview

package main

import (
	"log"
	"os"
	"runtime"

	webview "github.com/Ghibranalj/webview_go"
	"github.com/benitogf/ooo"
)

// runUI opens candyland's dashboard in a native desktop window (WebKitGTK on
// Linux/WSLg, WebView2 on Windows) pointing at the already-served SPA. The window
// owns the main thread until it's closed, then the server is shut down. When no
// window can open (--headless, no usable display, or a webview init failure) it
// degrades to the browser fallback (when --openBrowser is set) so a visible
// surface always materializes, and otherwise stays a plain server. setMode
// records the resolved surface (window/browser/headless) for /api/health.
func runUI(server *ooo.Server, spaURL string, headless bool, width, height int, debug, openBrowser bool, setMode func(string)) {
	if !headless {
		disp := resolveDisplay(runtime.GOOS, "", envMap())
		// Self-heal: a live display socket exists but the launcher's env didn't
		// name it — set the vars before webview init so the window can open.
		for k, v := range disp.Heal {
			log.Printf("candyland: healing display env %s=%s", k, v)
			_ = os.Setenv(k, v)
		}
		if disp.Usable && tryWindow(server, spaURL, width, height, debug, setMode) {
			return
		}
		if !disp.Usable {
			log.Printf("candyland: no usable display detected — not attempting a window")
		}
	}

	if openBrowser && openDashboard(spaURL) {
		setMode("browser")
		server.WaitClose()
		return
	}

	log.Printf("candyland: serving the UI headless at %s (open it in a browser)", spaURL)
	server.WaitClose()
}

// tryWindow attempts to open the desktop window, returning whether it opened. It
// guards webview init with a nil check and recover() so a webview failure in the
// detached process degrades to the browser fallback instead of crashing. On a
// successful open it records mode "window", runs the GUI loop until the window is
// closed, then shuts the server down.
func tryWindow(server *ooo.Server, spaURL string, width, height int, debug bool, setMode func(string)) (opened bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("candyland: webview init failed (%v); falling back to the browser", r)
			opened = false
		}
	}()

	waitForSPA(spaURL) // avoid a connection-refused first paint before the SPA listener binds
	w := webview.New(debug)
	if w == nil {
		log.Printf("candyland: webview.New returned nil; falling back to the browser")
		return false
	}
	defer w.Destroy()

	log.Printf("candyland: opening the desktop window → %s", spaURL)
	setMode("window")
	w.SetTitle("Candyland")
	w.SetSize(width, height, webview.HintNone)
	w.Navigate(spaURL)
	go server.WaitClose() // honor Ctrl-C / SIGTERM while the window is open
	w.Run()               // blocks on the GUI loop until the window is closed
	server.Close(os.Interrupt)
	return true
}
