package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPath(t *testing.T) {
	dir := t.TempDir()

	st := checkPath(dir)
	if !st.Exists || !st.Dir || !st.Readable || !st.Writable {
		t.Errorf("a fresh temp dir should be a readable+writable directory: %+v", st)
	}

	// A path that doesn't exist.
	if st := checkPath(filepath.Join(dir, "nope")); st.Exists {
		t.Errorf("missing path should not report Exists: %+v", st)
	}

	// A file is not a usable workspace folder (and never reports writable, since
	// the write probe only runs for directories).
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := checkPath(file); st.Dir || st.Writable {
		t.Errorf("a file should report neither Dir nor Writable: %+v", st)
	}
	// (Writability is a real create+remove probe, so it reflects what the backend
	// process can actually do — including that root bypasses mode bits. We don't
	// assert a mode-based negative here because the test process may be root.)
}

func TestListDirHidesDotfoldersAndSorts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zeta", "alpha", ".git"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range list.Entries {
		names = append(names, e.Name)
	}
	// Only non-hidden sub-directories, sorted; the file and .git are excluded.
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Errorf("listDir entries = %v, want [alpha zeta] (dirs only, sorted, no dotfolders/files)", names)
	}
	if list.Parent == "" {
		t.Error("a non-root dir should report a parent")
	}
}
