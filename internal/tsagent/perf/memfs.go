package perf

import (
	"strings"
	"sync"
	"time"

	"github.com/microsoft/typescript-go/internal/vfs"
)

// memFS is a minimal in-memory [vfs.FS] used as a disk-free sink for the
// tracing package. The compiler reads source files through its own host FS;
// tracing only ever calls WriteFile/AppendFile/ReadFile against this sink, so
// the remaining FS methods are stubbed. Directories are implicit: any path is
// reported as a directory so StartTracing's header write never fails for a
// missing parent.
type memFS struct {
	mu    sync.Mutex
	files map[string]string
}

var _ vfs.FS = (*memFS)(nil)

func newMemFS() *memFS {
	return &memFS{files: map[string]string{}}
}

func (*memFS) UseCaseSensitiveFileNames() bool { return true }

func (m *memFS) FileExists(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[path]
	return ok
}

func (m *memFS) ReadFile(path string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.files[path]
	return s, ok
}

func (m *memFS) WriteFile(path string, data string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = data
	return nil
}

func (m *memFS) AppendFile(path string, data string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] += data
	return nil
}

func (m *memFS) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.files {
		if k == path || strings.HasPrefix(k, path+"/") {
			delete(m.files, k)
		}
	}
	return nil
}

func (*memFS) Chtimes(string, time.Time, time.Time) error { return nil }

// DirectoryExists reports true for every path so the tracing writer treats the
// (virtual) trace directory as present.
func (*memFS) DirectoryExists(string) bool { return true }

func (*memFS) GetAccessibleEntries(string) vfs.Entries { return vfs.Entries{} }

func (*memFS) Stat(string) vfs.FileInfo { return nil }

func (*memFS) WalkDir(string, vfs.WalkDirFunc) error { return nil }

func (*memFS) Realpath(path string) string { return path }
