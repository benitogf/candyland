package main

import (
	"log"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// browserOpener opens a URL in the OS default browser. It is a package var so
// tests can stub the real exec away and assert the URL passed to it.
var browserOpener = openURLInBrowser

// openDashboard opens the dashboard URL in the default browser once the SPA
// listener accepts, so no connection-refused tab appears. Returns whether the
// opener was invoked successfully — the caller records mode "browser" on true,
// "headless" on false.
func openDashboard(spaURL string) bool {
	waitForSPA(spaURL)
	if err := browserOpener(spaURL); err != nil {
		log.Printf("candyland: could not open the dashboard in a browser (%v); staying headless at %s", err, spaURL)
		return false
	}
	log.Printf("candyland: opened the dashboard in your browser → %s", spaURL)
	return true
}

// openURLInBrowser launches the OS default browser at url. The command is
// started (not waited on) so it never blocks the caller.
func openURLInBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}

// waitForSPA blocks until the SPA port accepts a connection (the SPA server
// starts in a goroutine, so it may not be bound yet), giving up after ~2.5s so a
// stuck listener never wedges the window or browser fallback. Shared by the
// webview window path and the browser fallback.
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
