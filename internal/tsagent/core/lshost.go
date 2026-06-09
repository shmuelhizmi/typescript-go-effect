package core

import (
	"sync"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/ls"
	"github.com/microsoft/typescript-go/internal/ls/autoimport"
	"github.com/microsoft/typescript-go/internal/ls/lsconv"
	"github.com/microsoft/typescript-go/internal/ls/lsutil"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/sourcemap"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs/vfsmatch"
)

// lsHost implements ls.Host over a Workspace. Positions are standardized on
// UTF-8 encoding so LSP columns equal byte columns.
type lsHost struct {
	ws         *Workspace
	converters *lsconv.Converters

	registryOnce sync.Once
	registry     *autoimport.Registry

	lineMapMu  sync.Mutex
	lineMaps   map[string]*lsconv.LSPLineMap
	lineInfoMu sync.Mutex
	lineInfos  map[string]*sourcemap.ECMALineInfo
}

var _ ls.Host = (*lsHost)(nil)

func newLSHost(ws *Workspace) *lsHost {
	h := &lsHost{
		ws:        ws,
		lineMaps:  make(map[string]*lsconv.LSPLineMap),
		lineInfos: make(map[string]*sourcemap.ECMALineInfo),
	}
	h.converters = lsconv.NewConverters(lsproto.PositionEncodingKindUTF8, h.getLineMap)
	return h
}

func (h *lsHost) getLineMap(fileName string) *lsconv.LSPLineMap {
	h.lineMapMu.Lock()
	defer h.lineMapMu.Unlock()
	if lineMap, ok := h.lineMaps[fileName]; ok {
		return lineMap
	}
	content, ok := h.ReadFile(fileName)
	if !ok {
		return nil
	}
	lineMap := lsconv.ComputeLSPLineStarts(content)
	h.lineMaps[fileName] = lineMap
	return lineMap
}

func (h *lsHost) UseCaseSensitiveFileNames() bool {
	return h.ws.FS.UseCaseSensitiveFileNames()
}

func (h *lsHost) ReadFile(path string) (string, bool) {
	// Prefer the program's snapshot of the file when available.
	if file := h.ws.Program.GetSourceFile(path); file != nil {
		return file.Text(), true
	}
	return h.ws.FS.ReadFile(path)
}

func (h *lsHost) Converters() *lsconv.Converters {
	return h.converters
}

func (h *lsHost) GetPreferences(activeFile string) lsutil.UserPreferences {
	return lsutil.UserPreferences{}
}

func (h *lsHost) GetECMALineInfo(fileName string) *sourcemap.ECMALineInfo {
	h.lineInfoMu.Lock()
	defer h.lineInfoMu.Unlock()
	if info, ok := h.lineInfos[fileName]; ok {
		return info
	}
	content, ok := h.ReadFile(fileName)
	if !ok {
		return nil
	}
	info := sourcemap.CreateECMALineInfo(content, core.ComputeECMALineStarts(content))
	h.lineInfos[fileName] = info
	return info
}

func (h *lsHost) AutoImportRegistry() *autoimport.Registry {
	// Constructed lazily; only completions/import fixes consult it, and an
	// empty registry is a valid "nothing indexed yet" state.
	h.registryOnce.Do(func() {
		toPath := func(fileName string) tspath.Path {
			return tspath.ToPath(fileName, h.ws.Cwd, h.UseCaseSensitiveFileNames())
		}
		h.registry = autoimport.NewRegistry(toPath, lsutil.UserPreferences{})
	})
	return h.registry
}

func (h *lsHost) ReadDirectory(currentDir string, path string, extensions []string, excludes []string, includes []string, depth int) []string {
	return vfsmatch.ReadDirectory(h.ws.FS, currentDir, path, extensions, excludes, includes, depth)
}

func (h *lsHost) GetDirectories(path string) []string {
	return h.ws.FS.GetAccessibleEntries(path).Directories
}

func (h *lsHost) DirectoryExists(path string) bool {
	return h.ws.FS.DirectoryExists(path)
}

func (h *lsHost) FileExists(path string) bool {
	return h.ws.FS.FileExists(path)
}
