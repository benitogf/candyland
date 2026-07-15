package main

import (
	"os"
	"path/filepath"
	"testing"
)

// touchSocket creates an empty file at root+rel, standing in for a unix socket
// (resolveDisplay only stats the path, so a plain file suffices).
func touchSocket(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, nil, 0o600); err != nil {
		t.Fatalf("touch: %v", err)
	}
}

func TestResolveDisplayWindowsSkipsProbe(t *testing.T) {
	got := resolveDisplay("windows", t.TempDir(), map[string]string{})
	if !got.Usable {
		t.Error("windows should always be usable (WebView2 needs no display)")
	}
}

func TestResolveDisplayX11SetAndLive(t *testing.T) {
	root := t.TempDir()
	touchSocket(t, root, "/tmp/.X11-unix/X0")
	got := resolveDisplay("linux", root, map[string]string{"DISPLAY": ":0"})
	if !got.Usable {
		t.Error("DISPLAY set with a live socket should be usable")
	}
	if len(got.Heal) != 0 {
		t.Errorf("no heal expected when env already names the display, got %v", got.Heal)
	}
}

func TestResolveDisplayX11SetButDead(t *testing.T) {
	got := resolveDisplay("linux", t.TempDir(), map[string]string{"DISPLAY": ":0"})
	if got.Usable {
		t.Error("a set-but-dead DISPLAY must be treated as no display")
	}
}

func TestResolveDisplayWaylandLive(t *testing.T) {
	root := t.TempDir()
	touchSocket(t, root, "/run/user/1000/wayland-0")
	got := resolveDisplay("linux", root, map[string]string{
		"WAYLAND_DISPLAY": "wayland-0",
		"XDG_RUNTIME_DIR": "/run/user/1000",
	})
	if !got.Usable {
		t.Error("WAYLAND_DISPLAY with a live socket should be usable")
	}
}

func TestResolveDisplaySelfHealX0(t *testing.T) {
	root := t.TempDir()
	touchSocket(t, root, "/tmp/.X11-unix/X0")
	got := resolveDisplay("linux", root, map[string]string{})
	if !got.Usable {
		t.Fatal("env-empty with a live X0 socket should self-heal to usable")
	}
	if got.Heal["DISPLAY"] != ":0" {
		t.Errorf("heal DISPLAY = %q, want :0", got.Heal["DISPLAY"])
	}
}

func TestResolveDisplaySelfHealWSLg(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "/mnt/wslg"), 0o755); err != nil {
		t.Fatalf("mkdir wslg: %v", err)
	}
	touchSocket(t, root, "/mnt/wslg/runtime-dir/wayland-0")
	got := resolveDisplay("linux", root, map[string]string{})
	if !got.Usable {
		t.Fatal("WSLg markers should self-heal to usable")
	}
	if got.Heal["WAYLAND_DISPLAY"] != "wayland-0" || got.Heal["XDG_RUNTIME_DIR"] != wslgRuntimeDir {
		t.Errorf("WSLg heal = %v, want wayland-0 + %s", got.Heal, wslgRuntimeDir)
	}
}

func TestResolveDisplayNothing(t *testing.T) {
	got := resolveDisplay("linux", t.TempDir(), map[string]string{})
	if got.Usable {
		t.Error("no env and no socket should be not usable")
	}
}
