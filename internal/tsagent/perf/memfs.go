package perf

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/typescript-go/internal/vfs"
)

// diskTraceFS is a temporary, file-backed tracing sink. The tracing package
// writes very large trace/type JSON payloads; keeping them as Go strings
// doubles peak memory while the same payloads are decoded. This sink preserves
// the vfs.FS contract tracing needs while letting the OS page cache, not Go's
// heap, hold completed artifacts.
type diskTraceFS struct {
	root string
}

var _ vfs.FS = (*diskTraceFS)(nil)

func newDiskTraceFS() (*diskTraceFS, error) {
	root, err := os.MkdirTemp("", "tsagent-perf-trace-*")
	if err != nil {
		return nil, err
	}
	return &diskTraceFS{root: root}, nil
}

func (d *diskTraceFS) Cleanup() error {
	if d == nil || d.root == "" {
		return nil
	}
	return os.RemoveAll(d.root)
}

func (*diskTraceFS) UseCaseSensitiveFileNames() bool { return true }

func (d *diskTraceFS) FileExists(path string) bool {
	info, err := os.Stat(d.real(path))
	return err == nil && !info.IsDir()
}

func (d *diskTraceFS) ReadFile(path string) (string, bool) {
	data, err := os.ReadFile(d.real(path))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (d *diskTraceFS) WriteFile(path string, data string) error {
	real := d.real(path)
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		return err
	}
	return os.WriteFile(real, []byte(data), 0o600)
}

func (d *diskTraceFS) AppendFile(path string, data string) error {
	real := d.real(path)
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(real, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(data)
	return err
}

func (d *diskTraceFS) Remove(path string) error {
	return os.RemoveAll(d.real(path))
}

func (*diskTraceFS) Chtimes(string, time.Time, time.Time) error { return nil }

func (d *diskTraceFS) DirectoryExists(path string) bool {
	if path == traceDir {
		return true
	}
	info, err := os.Stat(d.real(path))
	return err == nil && info.IsDir()
}

func (*diskTraceFS) GetAccessibleEntries(string) vfs.Entries { return vfs.Entries{} }

func (d *diskTraceFS) Stat(path string) vfs.FileInfo {
	info, err := os.Stat(d.real(path))
	if err != nil {
		return nil
	}
	return info
}

func (*diskTraceFS) WalkDir(string, vfs.WalkDirFunc) error { return nil }

func (d *diskTraceFS) Realpath(path string) string { return d.real(path) }

func (d *diskTraceFS) real(path string) string {
	rel := strings.TrimLeft(strings.ReplaceAll(path, "\\", "/"), "/")
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." {
		return d.root
	}
	return filepath.Join(d.root, rel)
}
