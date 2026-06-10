// Package playground implements the in-memory compile pipeline behind the
// cmd/tsgo-wasm browser playground. It is kept free of syscall/js so it can
// be tested on the host.
package playground

import (
	"context"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/execute/tsc"
	"github.com/microsoft/typescript-go/internal/locale"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs"
)

type OutputFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type Diagnostic struct {
	File      string `json:"file"`
	Pos       int    `json:"pos"`
	End       int    `json:"end"`
	Line      int    `json:"line"`      // 0-based
	Character int    `json:"character"` // 0-based, UTF-16
	Code      int32  `json:"code"`
	Category  string `json:"category"`
	Message   string `json:"message"`
}

type CompileResult struct {
	Files       []OutputFile `json:"files"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type parseHost struct {
	fs  vfs.FS
	cwd string
}

func (h *parseHost) FS() vfs.FS                  { return h.fs }
func (h *parseHost) GetCurrentDirectory() string { return h.cwd }

// Compile builds the project at configPath over fs and returns the emitted
// outputs plus config and syntactic diagnostics. Semantic diagnostics are
// skipped unless checkSemantics is set; the playground's LSP session already
// pushes them to the editor.
func Compile(ctx context.Context, fs vfs.FS, cwd string, configPath string, defaultLibraryPath string, checkSemantics bool) *CompileResult {
	result := &CompileResult{Files: []OutputFile{}, Diagnostics: []Diagnostic{}}
	cache := &tsc.ExtendedConfigCache{}
	config, errs := tsoptions.GetParsedCommandLineOfConfigFile(configPath, nil, nil, &parseHost{fs: fs, cwd: cwd}, cache)
	if len(errs) > 0 || config == nil {
		result.addDiagnostics(errs)
		return result
	}
	host := compiler.NewCachedFSCompilerHost(cwd, fs, defaultLibraryPath, cache, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		Config:         config,
		Host:           host,
		SingleThreaded: core.TSTrue,
	})
	result.addDiagnostics(program.GetConfigFileParsingDiagnostics())
	for _, fileName := range config.FileNames() {
		file := program.GetSourceFile(fileName)
		if file == nil {
			continue
		}
		result.addDiagnostics(program.GetSyntacticDiagnostics(ctx, file))
		if checkSemantics {
			result.addDiagnostics(program.GetSemanticDiagnostics(ctx, file))
		}
	}
	program.Emit(ctx, compiler.EmitOptions{
		WriteFile: func(fileName string, text string, _ *compiler.WriteFileData) error {
			result.Files = append(result.Files, OutputFile{Name: fileName, Text: text})
			return nil
		},
	})
	return result
}

func (r *CompileResult) addDiagnostics(diags []*ast.Diagnostic) {
	for _, d := range diags {
		out := Diagnostic{
			Pos:      d.Pos(),
			End:      d.End(),
			Code:     d.Code(),
			Category: d.Category().Name(),
			Message:  d.Localize(locale.Default),
		}
		if file := d.File(); file != nil {
			out.File = file.FileName()
			line, character := scanner.GetECMALineAndUTF16CharacterOfPosition(file, d.Pos())
			out.Line = line
			out.Character = int(character)
		}
		r.Diagnostics = append(r.Diagnostics, out)
	}
}
