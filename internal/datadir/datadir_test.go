package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

// seedDB writes a minimal non-empty LevelDB-shaped directory at dir.
func seedDB(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seedDB MkdirAll(%q): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CURRENT"), []byte("MANIFEST-000001\n"), 0o644); err != nil {
		t.Fatalf("seedDB WriteFile: %v", err)
	}
}

func TestResolve_ExplicitOverrideWins(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom", "store")

	got := resolve(override, home, cwd)

	if got != override {
		t.Fatalf("override: got %q, want %q (verbatim)", got, override)
	}
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("override dir not created: %v", err)
	}
	// The home default must not have been created when an override is given.
	if _, err := os.Stat(filepath.Join(home, HomeSubPath)); !os.IsNotExist(err) {
		t.Fatalf("home default should not exist with override, stat err=%v", err)
	}
}

func TestResolve_EmptyFlagUsesHomeDefault(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir() // no legacy DB here

	got := resolve("", home, cwd)

	want := filepath.Join(home, HomeSubPath)
	if got != want {
		t.Fatalf("empty flag: got %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("home default dir not created: %v", err)
	}
}

func TestResolve_LegacyMigrationMovesData(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	legacy := filepath.Join(cwd, LegacyRelPath)
	seedDB(t, legacy)

	got := resolve("", home, cwd)

	want := filepath.Join(home, HomeSubPath)
	if got != want {
		t.Fatalf("migration: got path %q, want %q", got, want)
	}
	// Migrated file must be at the new location.
	if _, err := os.Stat(filepath.Join(want, "CURRENT")); err != nil {
		t.Fatalf("migrated CURRENT not found at target: %v", err)
	}
	// Legacy directory must be gone (renamed away).
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should be moved away, stat err=%v", err)
	}
}

func TestResolve_MissingLegacyGivesFreshDB(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir() // no legacy DB

	got := resolve("", home, cwd)

	want := filepath.Join(home, HomeSubPath)
	if got != want {
		t.Fatalf("fresh: got %q, want %q", got, want)
	}
	// Fresh dir exists but is empty (no migrated content).
	entries, err := os.ReadDir(want)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", want, err)
	}
	if len(entries) != 0 {
		t.Fatalf("fresh DB dir should be empty, has %d entries", len(entries))
	}
}

func TestResolve_TargetDBPresentDoesNotClobber(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	legacy := filepath.Join(cwd, LegacyRelPath)
	seedDB(t, legacy)

	// A DB already exists at the target; migration must not overwrite it.
	target := filepath.Join(home, HomeSubPath)
	seedDB(t, target)
	if err := os.WriteFile(filepath.Join(target, "MARKER"), []byte("new"), 0o644); err != nil {
		t.Fatalf("seed target marker: %v", err)
	}

	got := resolve("", home, cwd)

	if got != target {
		t.Fatalf("got %q, want %q", got, target)
	}
	if _, err := os.Stat(filepath.Join(target, "MARKER")); err != nil {
		t.Fatalf("existing target DB was clobbered, MARKER gone: %v", err)
	}
	// Legacy must be left untouched since we did not migrate.
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy should be untouched when target has a DB: %v", err)
	}
}

func TestResolve_MigrationFailureContinues(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	legacy := filepath.Join(cwd, LegacyRelPath)
	seedDB(t, legacy)

	// Make the target's parent un-creatable by planting a regular FILE where
	// the ~/.candyland directory needs to be. MkdirAll will fail → migration
	// is skipped, and resolve must still return the target path without panic.
	candylandDir := filepath.Join(home, ".candyland")
	if err := os.WriteFile(candylandDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}

	got := resolve("", home, cwd)

	want := filepath.Join(home, HomeSubPath)
	if got != want {
		t.Fatalf("migration failure: got %q, want %q (must still return target)", got, want)
	}
	// Legacy data should remain in place since the move could not happen.
	if _, err := os.Stat(filepath.Join(legacy, "CURRENT")); err != nil {
		t.Fatalf("legacy data should survive a failed migration: %v", err)
	}
}

func TestResolve_EmptyHomeFallsBackToLegacy(t *testing.T) {
	// resolve creates the legacy dir relative to the process cwd in this path;
	// chdir into a temp dir so the test does not write into the repo tree.
	t.Chdir(t.TempDir())
	cwd := t.TempDir()

	got := resolve("", "", cwd)

	if got != LegacyRelPath {
		t.Fatalf("empty home: got %q, want %q", got, LegacyRelPath)
	}
}
