package cmds

import (
	"context"
	"flag"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// refactor mv-symbol (§4.4): move ONE top-level declaration
// (function/class/interface/type/enum/sole-declarator const) to another
// project file. A thin wrapper around the planSymbolMove planner
// (symbolmove.go), shared with the `edit` command: this command resolves the
// target and destination, plans an append-at-end move, and finishes via the
// refactor transaction path. Refused (exit 4): declarations that reference
// file-local unexported symbols (unless --with-deps moves them along),
// default-exported or overloaded declarations.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "mv-symbol",
		Summary:      "Move a top-level declaration to another file, fixing imports both ways",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorMvSymbolFlags{}
			registerRefactorTargetFlags(fs, &f.target)
			registerRefactorTxFlags(fs, &f.tx)
			fs.StringVar(&f.to, "to", "", "destination file (project-relative or absolute)")
			fs.BoolVar(&f.create, "create", false, "create the destination file if it does not exist")
			fs.BoolVar(&f.withDeps, "with-deps", false, "move file-local unexported declarations the symbol references along with it")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorMvSymbol(ctx, ws, flags.(*refactorMvSymbolFlags), args)
		},
	})
}

type refactorMvSymbolFlags struct {
	target   refactorTargetFlags
	tx       refactorTxFlags
	to       string
	create   bool
	withDeps bool
}

func runRefactorMvSymbol(ctx context.Context, ws *core.Workspace, f *refactorMvSymbolFlags, args []string) (*core.TxResult, error) {
	if f.to == "" {
		return nil, cli.UsageErrorf("--to <file> is required")
	}
	target, rest, err := resolveRefactorTarget(ctx, ws, &f.target, args)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, cli.UsageErrorf("unexpected extra arguments: %s", strings.Join(rest, " "))
	}
	symbol := target.Symbol
	if symbol == nil || len(symbol.Declarations) == 0 {
		return nil, cli.NotFoundErrorf("target does not resolve to a symbol with declarations")
	}
	declNode := refactorDeletionNode(symbol.Declarations[0])

	// Destination resolution.
	destAbs := refactorResolveEditPath(ws, f.to)
	destFile := ws.Program.GetSourceFile(destAbs)
	creating := false
	if destFile == nil {
		if ws.FS.FileExists(destAbs) {
			return nil, cli.RefusedErrorf("destination %s exists but is not part of the program", ws.RelPath(destAbs))
		}
		if !f.create {
			return nil, cli.UsageErrorf("destination %s does not exist (pass --create to create it)", ws.RelPath(destAbs))
		}
		if !tspath.ContainsPath(ws.RootDir, destAbs, tspath.ComparePathsOptions{
			CurrentDirectory:          ws.Cwd,
			UseCaseSensitiveFileNames: ws.FS.UseCaseSensitiveFileNames(),
		}) {
			return nil, cli.UsageErrorf("destination %s is outside the project root (%s); --create only creates files inside the project", f.to, ws.RootDir)
		}
		creating = true
	}

	var es core.EditSet
	notes, err := planSymbolMove(ctx, ws, declNode, symbol,
		symbolMoveDest{fileAbs: destAbs, file: destFile, create: creating, insertPos: -1}, f.withDeps, &es)
	if err != nil {
		return nil, err
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, notes)
}
