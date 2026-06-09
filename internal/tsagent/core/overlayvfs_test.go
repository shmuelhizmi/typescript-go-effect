package core

import (
	"slices"
	"testing"

	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

func TestOverlayFS(t *testing.T) {
	t.Parallel()
	base := vfstest.FromMap(map[string]any{
		"/project/src/a.ts":    "base a",
		"/project/src/b.ts":    "base b",
		"/project/src/gone.ts": "deleted content",
	}, true /*useCaseSensitiveFileNames*/)
	overlay := NewOverlayFS(base, map[string]string{
		"/project/src/a.ts":        "overlay a",  // override
		"/project/src/new.ts":      "created",    // new file
		"/project/src/lib/util.ts": "moved here", // file in a directory that does not exist on disk
	}, []string{"/project/src/gone.ts"})

	// Read-through for untouched files.
	if text, ok := overlay.ReadFile("/project/src/b.ts"); !ok || text != "base b" {
		t.Errorf("read-through b.ts = %q, %v", text, ok)
	}
	// Overridden content wins.
	if text, ok := overlay.ReadFile("/project/src/a.ts"); !ok || text != "overlay a" {
		t.Errorf("override a.ts = %q, %v", text, ok)
	}
	// Created files are visible.
	if text, ok := overlay.ReadFile("/project/src/new.ts"); !ok || text != "created" {
		t.Errorf("created new.ts = %q, %v", text, ok)
	}
	if !overlay.FileExists("/project/src/new.ts") {
		t.Error("FileExists(new.ts) = false")
	}
	// Deleted files are gone.
	if overlay.FileExists("/project/src/gone.ts") {
		t.Error("FileExists(gone.ts) = true, want false")
	}
	if _, ok := overlay.ReadFile("/project/src/gone.ts"); ok {
		t.Error("ReadFile(gone.ts) ok = true, want false")
	}
	if overlay.Stat("/project/src/gone.ts") != nil {
		t.Error("Stat(gone.ts) != nil")
	}
	// Stat reflects overlay contents.
	if info := overlay.Stat("/project/src/a.ts"); info == nil || info.Size() != int64(len("overlay a")) {
		t.Errorf("Stat(a.ts) = %v", info)
	}
	// Realpath for overlay-only files returns the path itself.
	if rp := overlay.Realpath("/project/src/lib/util.ts"); rp != "/project/src/lib/util.ts" {
		t.Errorf("Realpath = %q", rp)
	}
	// Synthetic directories implied by overlay files exist.
	if !overlay.DirectoryExists("/project/src/lib") {
		t.Error("DirectoryExists(src/lib) = false")
	}

	// Directory listings merge base and overlay and drop deletions.
	entries := overlay.GetAccessibleEntries("/project/src")
	wantFiles := []string{"a.ts", "b.ts", "new.ts"}
	if !slices.Equal(entries.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", entries.Files, wantFiles)
	}
	if !slices.Contains(entries.Directories, "lib") {
		t.Errorf("Directories = %v, want to contain lib", entries.Directories)
	}
	libEntries := overlay.GetAccessibleEntries("/project/src/lib")
	if !slices.Equal(libEntries.Files, []string{"util.ts"}) {
		t.Errorf("lib Files = %v, want [util.ts]", libEntries.Files)
	}

	// The base FS is untouched.
	if text, ok := base.ReadFile("/project/src/a.ts"); !ok || text != "base a" {
		t.Errorf("base a.ts = %q, %v", text, ok)
	}
	if !base.FileExists("/project/src/gone.ts") {
		t.Error("base gone.ts disappeared")
	}
}
