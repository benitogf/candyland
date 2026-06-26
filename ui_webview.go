//go:build webview

package main

import (
	"log"
	"net"
	"net/url"
	"os"
	"time"

	webview "github.com/Ghibranalj/webview_go"
	"github.com/benitogf/ooo"
)

// runUI opens candyland's dashboard in a native desktop window (WebKitGTK on
// Linux/WSLg, WebView2 on Windows) pointing at the already-served SPA. The window
// owns the main thread until it's closed, then the server is shut down — the same
// shape as the mono template this is built on. Falls back to headless (serve-only)
// when --headless is set or no display is available, so a webview-tagged binary
// still runs as a plain server on a box with no GUI.
func runUI(server *ooo.Server, spaURL string, headless bool, width, height int, debug bool) {
	if headless || (os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		if !headless {
			log.Printf("candyland: no display detected — serving the UI at %s (open it in a browser)", spaURL)
		}
		server.WaitClose()
		return
	}

	log.Printf("candyland: opening the desktop window → %s", spaURL)
	waitForSPA(spaURL) // avoid a connection-refused first paint before the SPA listener binds
	w := webview.New(debug)
	defer w.Destroy()
	w.SetTitle("Candyland")
	w.SetSize(width, height, webview.HintNone)
	w.Navigate(spaURL)
	go server.WaitClose() // honor Ctrl-C / SIGTERM while the window is open
	w.Run()               // blocks on the GUI loop until the window is closed
	server.Close(os.Interrupt)
}

// waitForSPA blocks until the SPA port accepts a connection (the SPA server
// starts in a goroutine, so it may not be bound yet), giving up after ~2.5s so a
// stuck listener never wedges the window.
func waitForSPA(spaURL string) {
	u, err := url.Parse(spaURL)
	if err != nil || u.Host == "" {
		return
	}
	for range 50 {
		c, err := net.DialTimeout("tcp", u.Host, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
