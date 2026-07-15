package main

import (
	"os"
	"path/filepath"
	"strings"
)

// displayResult is the outcome of probing the machine for a usable desktop
// display. Usable reports whether a window can physically open; Heal carries the
// env vars that must be set (via os.Setenv) before webview init when the display
// exists but the launcher's env didn't name it — the self-heal path that fixes a
// window launched from a shell with DISPLAY unset.
type displayResult struct {
	Usable bool
	Heal   map[string]string
}

// resolveDisplay decides whether a desktop window can open by probing machine
// reality rather than trusting env alone. It is pure: goos selects the platform
// branch, root prefixes every absolute socket/marker path (""/"/" in production,
// a temp dir in tests), and env is the process environment as a map. On Linux it
// checks Wayland then X11 sockets, and — when env names no display — self-heals
// from a live socket (X0 or WSLg). Windows needs no display probe (WebView2), so
// it is always usable.
func resolveDisplay(goos, root string, env map[string]string) displayResult {
	if goos == "windows" {
		return displayResult{Usable: true}
	}

	if wayland := env["WAYLAND_DISPLAY"]; wayland != "" {
		if socketExists(filepath.Join(root, env["XDG_RUNTIME_DIR"], wayland)) {
			return displayResult{Usable: true}
		}
	}

	if display := env["DISPLAY"]; display != "" {
		// A set-but-dead DISPLAY is treated as no display, not a doomed attempt.
		if x11SocketExists(root, display) {
			return displayResult{Usable: true}
		}
		return displayResult{Usable: false}
	}

	// Env names no display — self-heal from a live socket if one exists.
	return healDisplay(root)
}

// healDisplay looks for a live display socket when the environment named none,
// returning the env vars to set so webview init can proceed. WSLg is preferred
// (its Wayland socket is the WSL desktop surface); a bare X0 socket is the X11
// fallback.
func healDisplay(root string) displayResult {
	if socketExists(filepath.Join(root, wslgRuntimeDir, "wayland-0")) && dirExists(filepath.Join(root, "/mnt/wslg")) {
		return displayResult{
			Usable: true,
			Heal: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0",
				"XDG_RUNTIME_DIR": wslgRuntimeDir,
			},
		}
	}

	if socketExists(filepath.Join(root, "/tmp/.X11-unix/X0")) {
		return displayResult{Usable: true, Heal: map[string]string{"DISPLAY": ":0"}}
	}

	return displayResult{Usable: false}
}

// wslgRuntimeDir is WSLg's runtime directory, which hosts its Wayland socket.
const wslgRuntimeDir = "/mnt/wslg/runtime-dir"

// x11SocketExists reports whether the X11 socket matching a DISPLAY value exists.
// DISPLAY is "[host]:<n>[.<screen>]"; the socket is /tmp/.X11-unix/X<n>.
func x11SocketExists(root, display string) bool {
	colon := strings.LastIndex(display, ":")
	if colon < 0 {
		return false
	}
	num := display[colon+1:]
	if dot := strings.Index(num, "."); dot >= 0 {
		num = num[:dot]
	}
	if num == "" {
		return false
	}
	return socketExists(filepath.Join(root, "/tmp/.X11-unix", "X"+num))
}

// socketExists reports whether path exists as a filesystem entry (a unix socket
// shows up via Stat). Any stat error counts as absent.
func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// envMap snapshots the process environment as a map, so resolveDisplay can be
// driven from the real os.Environ() in production and a fixture map in tests.
func envMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			out[kv[:eq]] = kv[eq+1:]
		}
	}
	return out
}
