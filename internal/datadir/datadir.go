// Package datadir resolves where candyland stores its embedded database and
// performs a one-time, best-effort migration from the legacy project-local
// `./db/data` layout to a fixed per-user home directory `~/.candyland/db`.
//
// The resolution and migration logic are pure functions that take the home
// directory and launch cwd as parameters so they can be unit-tested against
// temp directories — os.UserHomeDir() is only consulted by the thin Resolve
// wrapper, never deep inside the testable core.
package datadir

import (
	"errors"
	"log"
	"os"
	"path/filepath"
)

// LegacyRelPath is the project-local data directory candyland used before the
// fixed-home-directory layout. It is resolved relative to the launch cwd.
const LegacyRelPath = "db/data"

// HomeSubPath is the data directory under the user's home: ~/.candyland/db.
var HomeSubPath = filepath.Join(".candyland", "db")

// Resolve returns the data path candyland should open, performing the legacy
// migration as a side effect. An explicit flag value wins verbatim; an empty
// flag resolves to ~/.candyland/db. Directory creation and migration are
// best-effort — Resolve always returns a usable path and never an error that
// should abort startup (failures are logged).
func Resolve(flagVal string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home directory available: fall back to the legacy layout so the
		// process still has a usable path rather than aborting startup.
		log.Printf("datadir: os.UserHomeDir failed (%v); falling back to %q", err, LegacyRelPath)
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("datadir: os.Getwd failed (%v); migration from legacy path skipped", err)
		cwd = ""
	}
	return resolve(flagVal, home, cwd)
}

// resolve is the pure, testable core of Resolve. It does not touch the process
// environment (home and cwd are injected) and never returns an error: a usable
// path is always produced and any directory-creation or migration problem is
// logged and continued past.
func resolve(flagVal, home, cwd string) string {
	if flagVal != "" {
		// Explicit override wins verbatim. Still ensure it exists.
		ensureDir(flagVal)
		return flagVal
	}

	// Empty flag → home default. If no home is available, fall back to the
	// legacy relative path so the process still has somewhere to write.
	if home == "" {
		ensureDir(LegacyRelPath)
		return LegacyRelPath
	}

	target := filepath.Join(home, HomeSubPath)
	if cwd != "" {
		legacy := filepath.Join(cwd, LegacyRelPath)
		migrateLegacy(legacy, target)
	}
	ensureDir(target)
	return target
}

// ensureDir creates dir (and parents) best-effort. A failure is logged and
// swallowed — startup must continue toward a usable path.
func ensureDir(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("datadir: could not create data directory %q (%v); continuing", dir, err)
	}
}

// migrateLegacy moves a legacy project-local DB at legacy to target, but only
// when legacy holds a DB AND target does not yet. Any failure is logged and
// swallowed so the caller continues with a fresh DB at target. The parent of
// target is created first so the rename can land.
func migrateLegacy(legacy, target string) {
	if !hasDB(legacy) {
		return // nothing to migrate
	}
	if hasDB(target) {
		return // already migrated / new DB present; never clobber it
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		log.Printf("datadir: migration skipped, cannot create %q (%v); continuing with fresh DB", filepath.Dir(target), err)
		return
	}
	if err := os.Rename(legacy, target); err != nil {
		log.Printf("datadir: migration of %q → %q failed (%v); continuing with fresh DB", legacy, target, err)
		return
	}
	log.Printf("datadir: migrated legacy data %q → %q", legacy, target)
}

// hasDB reports whether path is a non-empty directory (a LevelDB store is a
// directory of files). A missing path, a non-directory, or an empty directory
// all count as "no DB".
func hasDB(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("datadir: could not inspect %q (%v); treating as absent", path, err)
		}
		return false
	}
	return len(entries) > 0
}
