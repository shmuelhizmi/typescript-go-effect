package core

import (
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs"
)

// overlayFS is a vfs.FS decorator that layers in-memory file contents (and
// deletions) over a base file system. It is the substrate for speculative
// program builds: the transaction engine's diagnostics gate and (later)
// `check --with-diff` construct a second Program against an overlay without
// touching disk.
//
// ReadFile/FileExists/Stat/Realpath/DirectoryExists/GetAccessibleEntries
// consult the overlay first so that created, edited, renamed, and deleted
// files are all visible to tsconfig file enumeration (which goes through
// GetAccessibleEntries via vfsmatch). WalkDir and the mutating methods
// delegate to the base FS.
type overlayFS struct {
	base    vfs.FS
	files   map[string]overlayFile // canonical path → file
	deleted map[string]bool        // canonical path → true
}

type overlayFile struct {
	path    string // normalized (display-cased) path
	content string
}

var _ vfs.FS = (*overlayFS)(nil)

// NewOverlayFS layers contents (path → new content) and deleted paths over
// base. Paths must be absolute; they are normalized internally.
func NewOverlayFS(base vfs.FS, contents map[string]string, deleted []string) vfs.FS {
	o := &overlayFS{
		base:    base,
		files:   make(map[string]overlayFile, len(contents)),
		deleted: make(map[string]bool, len(deleted)),
	}
	for path, content := range contents {
		path = tspath.NormalizePath(path)
		o.files[o.canonical(path)] = overlayFile{path: path, content: content}
	}
	for _, path := range deleted {
		key := o.canonical(tspath.NormalizePath(path))
		if _, ok := o.files[key]; !ok {
			o.deleted[key] = true
		}
	}
	return o
}

func (o *overlayFS) canonical(path string) string {
	return tspath.GetCanonicalFileName(path, o.base.UseCaseSensitiveFileNames())
}

func (o *overlayFS) UseCaseSensitiveFileNames() bool {
	return o.base.UseCaseSensitiveFileNames()
}

func (o *overlayFS) FileExists(path string) bool {
	key := o.canonical(tspath.NormalizePath(path))
	if _, ok := o.files[key]; ok {
		return true
	}
	if o.deleted[key] {
		return false
	}
	return o.base.FileExists(path)
}

func (o *overlayFS) ReadFile(path string) (contents string, ok bool) {
	key := o.canonical(tspath.NormalizePath(path))
	if file, ok := o.files[key]; ok {
		return file.content, true
	}
	if o.deleted[key] {
		return "", false
	}
	return o.base.ReadFile(path)
}

func (o *overlayFS) DirectoryExists(path string) bool {
	if o.base.DirectoryExists(path) {
		return true
	}
	// Synthetic directories implied by overlay files (e.g. rename targets in
	// a directory that does not exist on disk yet).
	prefix := o.dirPrefix(path)
	for key := range o.files {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// dirPrefix returns the canonical path of dir with a trailing slash, for
// "is under this directory" prefix tests against canonical keys.
func (o *overlayFS) dirPrefix(dir string) string {
	prefix := o.canonical(tspath.NormalizePath(dir))
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

func (o *overlayFS) GetAccessibleEntries(path string) vfs.Entries {
	entries := o.base.GetAccessibleEntries(path)
	dir := tspath.NormalizePath(path)
	prefix := o.dirPrefix(dir)

	// Drop deleted files.
	if len(o.deleted) > 0 {
		files := entries.Files[:0:0]
		for _, name := range entries.Files {
			if !o.deleted[o.canonical(tspath.CombinePaths(dir, name))] {
				files = append(files, name)
			}
		}
		entries.Files = files
	}

	// Add overlay files directly in this directory and synthetic
	// subdirectories for overlay files nested deeper.
	seenFiles := make(map[string]bool, len(entries.Files))
	for _, name := range entries.Files {
		seenFiles[o.canonical(name)] = true
	}
	seenDirs := make(map[string]bool, len(entries.Directories))
	for _, name := range entries.Directories {
		seenDirs[o.canonical(name)] = true
	}
	added := false
	for key, file := range o.files {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok || rest == "" {
			continue
		}
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			child := file.path[len(file.path)-len(rest) : len(file.path)-len(rest)+slash]
			if !seenDirs[o.canonical(child)] {
				seenDirs[o.canonical(child)] = true
				entries.Directories = append(entries.Directories, child)
				added = true
			}
		} else {
			child := file.path[len(file.path)-len(rest):]
			if !seenFiles[o.canonical(child)] {
				seenFiles[o.canonical(child)] = true
				entries.Files = append(entries.Files, child)
				added = true
			}
		}
	}
	if added {
		slices.Sort(entries.Files)
		slices.Sort(entries.Directories)
	}
	return entries
}

func (o *overlayFS) Stat(path string) vfs.FileInfo {
	key := o.canonical(tspath.NormalizePath(path))
	if file, ok := o.files[key]; ok {
		return &overlayFileInfo{name: tspath.GetBaseFileName(file.path), size: int64(len(file.content))}
	}
	if o.deleted[key] {
		return nil
	}
	return o.base.Stat(path)
}

func (o *overlayFS) Realpath(path string) string {
	key := o.canonical(tspath.NormalizePath(path))
	if file, ok := o.files[key]; ok {
		return file.path
	}
	if o.deleted[key] {
		return tspath.NormalizePath(path)
	}
	return o.base.Realpath(path)
}

// WalkDir delegates to the base FS: overlay entries are surfaced through
// GetAccessibleEntries (which is what program construction uses).
func (o *overlayFS) WalkDir(root string, walkFn vfs.WalkDirFunc) error {
	return o.base.WalkDir(root, walkFn)
}

func (o *overlayFS) WriteFile(path string, data string) error {
	return o.base.WriteFile(path, data)
}

func (o *overlayFS) AppendFile(path string, data string) error {
	return o.base.AppendFile(path, data)
}

func (o *overlayFS) Remove(path string) error {
	return o.base.Remove(path)
}

func (o *overlayFS) Chtimes(path string, aTime time.Time, mTime time.Time) error {
	return o.base.Chtimes(path, aTime, mTime)
}

type overlayFileInfo struct {
	name string
	size int64
}

var _ fs.FileInfo = (*overlayFileInfo)(nil)

func (i *overlayFileInfo) Name() string       { return i.name }
func (i *overlayFileInfo) Size() int64        { return i.size }
func (i *overlayFileInfo) Mode() fs.FileMode  { return 0o644 }
func (i *overlayFileInfo) ModTime() time.Time { return time.Time{} }
func (i *overlayFileInfo) IsDir() bool        { return false }
func (i *overlayFileInfo) Sys() any           { return nil }
