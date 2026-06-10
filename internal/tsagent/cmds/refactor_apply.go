package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	icore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// refactor apply-edits (§4.4): the universal "commit" primitive. Applies a
// machine-generated edit set (the JSON schema documented by
// `check --with-edits`) — or, with --diff, a unified diff — as one
// transaction.

func init() {
	cli.Register(cli.Command{
		Family:       "refactor",
		Name:         "apply-edits",
		Summary:      "Apply a machine-generated edit set (or unified diff) as one transaction",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &refactorApplyEditsFlags{}
			registerRefactorTxFlags(fs, &f.tx)
			fs.BoolVar(&f.diff, "diff", false, "treat the input as a unified diff instead of edits JSON")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runRefactorApplyEdits(ctx, ws, flags.(*refactorApplyEditsFlags), args)
		},
	})
}

type refactorApplyEditsFlags struct {
	tx   refactorTxFlags
	diff bool

	// stdin is the reader used when the input argument is `-`; overridable by
	// tests. Defaults to os.Stdin.
	stdin io.Reader
}

// refactorEditsJSON is the edits JSON schema shared with `check --with-edits`
// (the JSON form of core.EditSet, with byte-offset ranges).
type refactorEditsJSON struct {
	Edits []struct {
		File  string `json:"file"`
		Edits []struct {
			Pos     int    `json:"pos"`
			End     int    `json:"end"`
			NewText string `json:"newText"`
		} `json:"edits"`
	} `json:"edits"`
	Ops []struct {
		Kind    string `json:"kind"`
		Path    string `json:"path"`
		NewPath string `json:"newPath,omitempty"`
		Content string `json:"content,omitempty"`
	} `json:"ops,omitempty"`
}

func runRefactorApplyEdits(ctx context.Context, ws *core.Workspace, f *refactorApplyEditsFlags, args []string) (*core.TxResult, error) {
	if len(args) != 1 {
		return nil, cli.UsageErrorf("refactor apply-edits takes exactly one input argument (a file path, or - for stdin)")
	}
	text, err := refactorReadInput(ws, f.stdin, args[0])
	if err != nil {
		return nil, err
	}

	var es core.EditSet
	if f.diff {
		es, err = refactorEditSetFromDiff(ws, text)
	} else {
		es, err = refactorEditSetFromJSON(ws, text)
	}
	if err != nil {
		return nil, err
	}
	if es.IsEmpty() {
		return nil, cli.UsageErrorf("input contains no edits or file operations")
	}
	return finishRefactorTx(ctx, ws, es, &f.tx, nil)
}

// refactorReadInput reads the input argument: `-` for stdin, otherwise a file
// path resolved against the invocation cwd (then the project root).
func refactorReadInput(ws *core.Workspace, stdin io.Reader, arg string) (string, error) {
	if arg == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return string(data), nil
	}
	if text, ok := ws.FS.ReadFile(ws.AbsPath(arg)); ok {
		return text, nil
	}
	if text, ok := ws.FS.ReadFile(tspath.GetNormalizedAbsolutePath(arg, ws.RootDir)); ok {
		return text, nil
	}
	return "", cli.NotFoundErrorf("cannot read %s", arg)
}

func refactorEditSetFromJSON(ws *core.Workspace, text string) (core.EditSet, error) {
	var parsed refactorEditsJSON
	var es core.EditSet
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return es, cli.UsageErrorf("invalid edits JSON: %v", err)
	}
	for _, fe := range parsed.Edits {
		fileEdit := core.FileEdit{FileName: refactorResolveEditPath(ws, fe.File)}
		for _, e := range fe.Edits {
			fileEdit.Edits = append(fileEdit.Edits, icore.TextChange{
				TextRange: icore.NewTextRange(e.Pos, e.End),
				NewText:   e.NewText,
			})
		}
		es.Edits = append(es.Edits, fileEdit)
	}
	for _, op := range parsed.Ops {
		kind := core.FileOpKind(op.Kind)
		switch kind {
		case core.FileOpCreate, core.FileOpDelete, core.FileOpRename:
		default:
			return es, cli.UsageErrorf("invalid op kind %q (want create, delete, or rename)", op.Kind)
		}
		newPath := ""
		if op.NewPath != "" {
			newPath = refactorResolveEditPath(ws, op.NewPath)
		}
		es.Ops = append(es.Ops, core.FileOp{
			Kind:    kind,
			Path:    refactorResolveEditPath(ws, op.Path),
			NewPath: newPath,
			Content: op.Content,
		})
	}
	return es, nil
}

// refactorEditSetFromDiff converts a unified diff into whole-file replacement
// edits (plus create/delete ops) by applying each file patch to the current
// content.
func refactorEditSetFromDiff(ws *core.Workspace, text string) (core.EditSet, error) {
	var es core.EditSet
	patches, err := core.ParseUnifiedDiff(text)
	if err != nil {
		return es, err
	}
	for rel, patch := range patches {
		abs := refactorResolveEditPath(ws, rel)
		switch {
		case patch.IsDelete:
			if !ws.FS.FileExists(abs) {
				return es, cli.NotFoundErrorf("patch deletes %s, which does not exist", rel)
			}
			es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpDelete, Path: abs})
		case patch.IsNew:
			if ws.FS.FileExists(abs) {
				return es, cli.UsageErrorf("patch creates %s, which already exists", rel)
			}
			newText, err := patch.Apply("")
			if err != nil {
				return es, fmt.Errorf("applying patch for %s: %w", rel, err)
			}
			es.Ops = append(es.Ops, core.FileOp{Kind: core.FileOpCreate, Path: abs, Content: newText})
		default:
			oldText, ok := ws.FS.ReadFile(abs)
			if !ok {
				return es, cli.NotFoundErrorf("patch edits %s, which does not exist", rel)
			}
			newText, err := patch.Apply(oldText)
			if err != nil {
				return es, fmt.Errorf("applying patch for %s: %w", rel, err)
			}
			es.Edits = append(es.Edits, core.FileEdit{
				FileName: abs,
				Edits:    []icore.TextChange{{TextRange: icore.NewTextRange(0, len(oldText)), NewText: newText}},
			})
		}
	}
	return es, nil
}

// refactorResolveEditPath resolves an edit-set path: project root first
// (machine-generated paths are root-relative), invocation cwd as a fallback
// for existing files.
func refactorResolveEditPath(ws *core.Workspace, rel string) string {
	fromRoot := tspath.GetNormalizedAbsolutePath(rel, ws.RootDir)
	if ws.FS.FileExists(fromRoot) {
		return fromRoot
	}
	fromCwd := tspath.GetNormalizedAbsolutePath(rel, ws.Cwd)
	if ws.FS.FileExists(fromCwd) {
		return fromCwd
	}
	return fromRoot
}
