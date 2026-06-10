package core

import (
	"fmt"
	"os"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/execute/tsc"
	"github.com/microsoft/typescript-go/internal/ls"
	"github.com/microsoft/typescript-go/internal/ls/lsconv"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

// Workspace is the per-invocation context passed to every command handler.
type Workspace struct {
	ConfigPath string
	// RootDir is the directory of the resolved tsconfig. Display paths and
	// symbol IDs are rendered relative to it so they stay stable regardless
	// of the invocation directory.
	RootDir string
	Cwd     string
	FS      vfs.FS
	Config  *tsoptions.ParsedCommandLine
	Program *compiler.Program
	LS      *ls.LanguageService
	Conv    *lsconv.Converters
}

// Options configures workspace construction.
type Options struct {
	// Project is a tsconfig.json path or a directory containing one. When
	// empty, tsconfig.json is discovered by walking up from Cwd.
	Project string
	// Cwd defaults to the process working directory.
	Cwd string
	// FS defaults to the bundled-lib-wrapped OS file system. Tests inject an
	// in-memory FS here.
	FS vfs.FS
	// SingleThreaded forces single-threaded program construction (tests).
	SingleThreaded bool
}

type parseConfigHost struct {
	fs  vfs.FS
	cwd string
}

var _ tsoptions.ParseConfigHost = (*parseConfigHost)(nil)

func (h *parseConfigHost) FS() vfs.FS                  { return h.fs }
func (h *parseConfigHost) GetCurrentDirectory() string { return h.cwd }

// NewWorkspace builds a fresh Program (and language service) for the
// resolved tsconfig.
func NewWorkspace(opts Options) (*Workspace, error) {
	fs := opts.FS
	if fs == nil {
		fs = bundled.WrapFS(osvfs.FS())
	}
	cwd := opts.Cwd
	if cwd == "" {
		osCwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current directory: %w", err)
		}
		cwd = osCwd
	}
	cwd = tspath.NormalizePath(cwd)

	configPath, err := resolveConfigPath(fs, cwd, opts.Project)
	if err != nil {
		return nil, err
	}

	configCache := &tsc.ExtendedConfigCache{}
	parseHost := &parseConfigHost{fs: fs, cwd: cwd}
	config, diags := tsoptions.GetParsedCommandLineOfConfigFile(configPath, nil, nil, parseHost, configCache)
	if config == nil || len(diags) > 0 {
		return nil, fmt.Errorf("failed to parse %s: %s", configPath, diagnosticMessages(diags))
	}

	host := compiler.NewCachedFSCompilerHost(cwd, fs, bundled.LibPath(), configCache, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		Config:         config,
		Host:           host,
		SingleThreaded: core.IfElse(opts.SingleThreaded, core.TSTrue, core.TSUnknown),
	})

	ws := &Workspace{
		ConfigPath: configPath,
		RootDir:    tspath.GetDirectoryPath(configPath),
		Cwd:        cwd,
		FS:         fs,
		Config:     config,
		Program:    program,
	}
	lsHost := newLSHost(ws)
	ws.Conv = lsHost.Converters()
	projectPath := tspath.ToPath(configPath, cwd, fs.UseCaseSensitiveFileNames())
	ws.LS = ls.NewLanguageService(projectPath, program, lsHost, "")
	return ws, nil
}

func resolveConfigPath(fs vfs.FS, cwd string, project string) (string, error) {
	if project == "" {
		if config, ok := findConfigUpwards(fs, cwd); ok {
			return config, nil
		}
		return "", fmt.Errorf("no tsconfig.json found walking up from %s (use --project)", cwd)
	}
	abs := tspath.GetNormalizedAbsolutePath(project, cwd)
	if fs.DirectoryExists(abs) {
		// A directory without its own tsconfig.json is covered by the nearest
		// ancestor config (same discovery as the cwd default).
		if config, ok := findConfigUpwards(fs, abs); ok {
			return config, nil
		}
		return "", fmt.Errorf("no tsconfig.json in %s or any parent directory (walked up to the filesystem root)", abs)
	}
	if fs.FileExists(abs) {
		return abs, nil
	}
	return "", fmt.Errorf("project %s does not exist", abs)
}

// findConfigUpwards walks from start to the filesystem root looking for a
// tsconfig.json.
func findConfigUpwards(fs vfs.FS, start string) (string, bool) {
	for dir := start; ; {
		candidate := tspath.CombinePaths(dir, "tsconfig.json")
		if fs.FileExists(candidate) {
			return candidate, true
		}
		parent := tspath.GetDirectoryPath(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func diagnosticMessages(diags []*ast.Diagnostic) string {
	msgs := make([]string, 0, len(diags))
	for _, d := range diags {
		msgs = append(msgs, d.String())
	}
	return strings.Join(msgs, "; ")
}

// FileOf resolves a user-supplied path to a source file in the program.
// Relative paths are tried against the invocation cwd first, then against
// the project root (the form used by symbol IDs and displayed paths).
func (w *Workspace) FileOf(arg string) (*ast.SourceFile, error) {
	if file := w.Program.GetSourceFile(tspath.GetNormalizedAbsolutePath(arg, w.Cwd)); file != nil {
		return file, nil
	}
	if file := w.Program.GetSourceFile(tspath.GetNormalizedAbsolutePath(arg, w.RootDir)); file != nil {
		return file, nil
	}
	return nil, fmt.Errorf("file %s is not part of the program: %w", arg, ErrNotFound)
}

// URI converts a file name to an LSP document URI.
func (w *Workspace) URI(fileName string) lsproto.DocumentUri {
	return lsconv.FileNameToDocumentURI(fileName)
}

// RelPath renders a file name relative to the project root for display and
// for symbol IDs.
func (w *Workspace) RelPath(fileName string) string {
	return tspath.ConvertToRelativePath(fileName, tspath.ComparePathsOptions{
		CurrentDirectory:          w.RootDir,
		UseCaseSensitiveFileNames: w.FS.UseCaseSensitiveFileNames(),
	})
}

// AbsPath resolves a possibly-relative user path against the workspace cwd.
func (w *Workspace) AbsPath(arg string) string {
	return tspath.GetNormalizedAbsolutePath(arg, w.Cwd)
}

// PosToLineCol converts a byte offset to a 1-based line and 1-based UTF-8
// byte column.
func (w *Workspace) PosToLineCol(file *ast.SourceFile, pos int) (line int, col int) {
	l, byteOffset := scanner.GetECMALineAndByteOffsetOfPosition(file, pos)
	return l + 1, byteOffset + 1
}

// LineColToPos converts a 1-based line and 1-based UTF-8 byte column to a
// byte offset.
func (w *Workspace) LineColToPos(file *ast.SourceFile, line int, col int) (int, error) {
	lineStarts := file.ECMALineMap()
	if line < 1 || line > len(lineStarts) {
		return 0, fmt.Errorf("line %d out of range for %s (1-%d): %w", line, file.FileName(), len(lineStarts), ErrInvalidArgument)
	}
	if col < 1 {
		return 0, fmt.Errorf("column %d out of range (columns are 1-based): %w", col, ErrInvalidArgument)
	}
	pos := int(lineStarts[line-1]) + col - 1
	if pos > len(file.Text()) {
		return 0, fmt.Errorf("position %d:%d is past the end of %s: %w", line, col, file.FileName(), ErrInvalidArgument)
	}
	return pos, nil
}
