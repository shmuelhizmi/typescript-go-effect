package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// analyze barrel-cost (§4.5): find barrel files (re-export hubs) and measure
// what importing them costs (transitive files/lines pulled in), with
// suggested direct-import rewrites per importer and an optional --fix-plan
// EditSet.

func init() {
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "barrel-cost",
		Summary:      "Barrel files, their transitive import cost, and direct-import rewrites",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &barrelCostFlags{}
			fs.IntVar(&f.minReexports, "min-reexports", 3, "minimum re-export statements for a file to count as a barrel")
			fs.BoolVar(&f.fixPlan, "fix-plan", false, "emit an EditSet replacing barrel imports with direct imports (for refactor apply-edits)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeBarrelCost(ctx, ws, flags.(*barrelCostFlags), args)
		},
	})
}

type barrelCostFlags struct {
	minReexports int
	fixPlan      bool
}

// BarrelImporter is one file importing a barrel, with the names it actually
// uses and the suggested direct imports.
type BarrelImporter struct {
	File       string   `json:"file"`
	UsedNames  []string `json:"usedNames"`
	Suggestion string   `json:"suggestion,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// BarrelReport is the cost report of one barrel file.
type BarrelReport struct {
	File            string            `json:"file"`
	Reexports       int               `json:"reexports"`
	ExportedNames   int               `json:"exportedNames"`
	TransitiveFiles int               `json:"transitiveFiles"`
	TransitiveLines int               `json:"transitiveLines"`
	Importers       []*BarrelImporter `json:"importers"`
}

// BarrelFixPlan is the EditSet-shaped rewrite plan (--fix-plan), consumable
// by `refactor apply-edits`.
type BarrelFixPlan struct {
	Edits []DeadCodeFixFile `json:"edits"`
	Ops   []any             `json:"ops"`
	Notes []string          `json:"notes,omitempty"`
}

// BarrelCostResult is the `analyze barrel-cost` result.
type BarrelCostResult struct {
	Barrels []*BarrelReport `json:"barrels"`
	FixPlan *BarrelFixPlan  `json:"fixPlan,omitempty"`
}

var _ cli.Texter = (*BarrelCostResult)(nil)

func (r *BarrelCostResult) WriteText(w io.Writer) error {
	for _, b := range r.Barrels {
		if _, err := fmt.Fprintf(w, "%s  %d re-exports, %d exported names, pulls %d files / %d lines\n",
			b.File, b.Reexports, b.ExportedNames, b.TransitiveFiles, b.TransitiveLines); err != nil {
			return err
		}
		for _, imp := range b.Importers {
			line := fmt.Sprintf("  %s uses %d name(s): %s", imp.File, len(imp.UsedNames), strings.Join(imp.UsedNames, ", "))
			if imp.Suggestion != "" {
				line += "\n    -> " + strings.ReplaceAll(imp.Suggestion, "\n", "\n    -> ")
			}
			if imp.Note != "" {
				line += "\n    note: " + imp.Note
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(w, "total: %d barrel(s)\n", len(r.Barrels)); err != nil {
		return err
	}
	if r.FixPlan != nil {
		return writeJSONSection(w, "fix plan", r.FixPlan)
	}
	return nil
}

func runAnalyzeBarrelCost(ctx context.Context, ws *core.Workspace, flags *barrelCostFlags, args []string) (*BarrelCostResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}
	graph := core.BuildImportGraph(ws)
	result := &BarrelCostResult{Barrels: []*BarrelReport{}}
	var plan *BarrelFixPlan
	if flags.fixPlan {
		plan = &BarrelFixPlan{Edits: []DeadCodeFixFile{}, Ops: []any{}}
	}

	for _, file := range files {
		reexports, total := barrelStatementCounts(file)
		if reexports < flags.minReexports || total == 0 || reexports*5 < total*4 { // <80%
			continue
		}
		report := &BarrelReport{
			File:      ws.RelPath(file.FileName()),
			Reexports: reexports,
			Importers: []*BarrelImporter{},
		}
		report.TransitiveFiles, report.TransitiveLines = barrelTransitiveCost(ws, graph, file)

		exports := barrelExportOrigins(ctx, ws, file)
		report.ExportedNames = len(exports)

		for _, importer := range barrelImporters(ws, file) {
			row, edits := barrelImporterReport(ws, importer, file, exports)
			report.Importers = append(report.Importers, row)
			if plan == nil {
				continue
			}
			if len(edits) == 0 {
				plan.Notes = append(plan.Notes, fmt.Sprintf("%s: skipped (%s)", row.File, row.Note))
				continue
			}
			plan.Edits = append(plan.Edits, DeadCodeFixFile{File: row.File, Edits: edits})
		}
		slices.SortFunc(report.Importers, func(a, b *BarrelImporter) int { return strings.Compare(a.File, b.File) })
		result.Barrels = append(result.Barrels, report)
	}

	slices.SortFunc(result.Barrels, func(a, b *BarrelReport) int {
		if d := b.TransitiveLines - a.TransitiveLines; d != 0 {
			return d
		}
		return strings.Compare(a.File, b.File)
	})
	if plan != nil {
		slices.SortFunc(plan.Edits, func(a, b DeadCodeFixFile) int { return strings.Compare(a.File, b.File) })
		result.FixPlan = plan
	}
	return result, nil
}

// barrelStatementCounts counts re-export statements (export ... from) and all
// top-level statements of a file.
func barrelStatementCounts(file *ast.SourceFile) (reexports int, total int) {
	for _, statement := range file.Statements.Nodes {
		total++
		if statement.Kind == ast.KindExportDeclaration && statement.AsExportDeclaration().ModuleSpecifier != nil {
			reexports++
		}
	}
	return reexports, total
}

// barrelTransitiveCost BFSes the import graph from the barrel and totals the
// reachable files (excluding the barrel) and their line counts (program
// files only; unresolvable/external files count as files with 0 lines).
func barrelTransitiveCost(ws *core.Workspace, graph *core.ImportGraph, barrel *ast.SourceFile) (filesCount int, lines int) {
	start, ok := graph.NodeID(barrel.FileName())
	if !ok {
		return 0, 0
	}
	seen := map[int]bool{start: true}
	queue := []int{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, edge := range graph.OutEdges(id) {
			if seen[edge.To] {
				continue
			}
			seen[edge.To] = true
			queue = append(queue, edge.To)
			filesCount++
			if f := ws.Program.GetSourceFile(graph.Nodes[edge.To].FileName); f != nil {
				lines += len(f.ECMALineMap())
			}
		}
	}
	return filesCount, lines
}

// barrelExportOrigin is where one exported name of a barrel actually lives.
type barrelExportOrigin struct {
	file       *ast.SourceFile // declaration file (nil when unresolvable)
	sourceName string          // name to import from the origin file
}

// barrelExportOrigins maps each exported name of the barrel to its origin
// declaration file, resolving aliases through the checker (which follows
// named re-export chains and `export *` in one step).
func barrelExportOrigins(ctx context.Context, ws *core.Workspace, barrel *ast.SourceFile) map[string]barrelExportOrigin {
	origins := make(map[string]barrelExportOrigin)
	c, done := ws.Program.GetTypeCheckerForFile(ctx, barrel)
	defer done()
	moduleSymbol := barrel.AsNode().Symbol()
	if moduleSymbol == nil {
		return origins
	}
	for _, symbol := range c.GetExportsOfModule(c.GetMergedSymbol(moduleSymbol)) {
		if strings.HasPrefix(symbol.Name, ast.InternalSymbolNamePrefix) {
			continue
		}
		target := symbol
		if symbol.Flags&ast.SymbolFlagsAlias != 0 {
			if resolved := c.GetAliasedSymbol(symbol); resolved != nil {
				target = resolved
			}
		}
		decl := target.ValueDeclaration
		if decl == nil && len(target.Declarations) > 0 {
			decl = target.Declarations[0]
		}
		origin := barrelExportOrigin{sourceName: target.Name}
		if decl != nil {
			declFile := ast.GetSourceFileOfNode(decl)
			if declFile != nil && declFile != barrel && refactorIsProjectSourceNode(ws, decl) {
				origin.file = declFile
			}
		}
		origins[symbol.Name] = origin
	}
	return origins
}

// barrelImporters lists project files with at least one import declaration
// resolving to the barrel.
func barrelImporters(ws *core.Workspace, barrel *ast.SourceFile) []*ast.SourceFile {
	all, err := projectFiles(ws, nil)
	if err != nil {
		return nil
	}
	var importers []*ast.SourceFile
	for _, file := range all {
		if file == barrel {
			continue
		}
		if len(refactorImportsResolvingTo(ws, file, barrel.FileName())) > 0 {
			importers = append(importers, file)
		}
	}
	return importers
}

// barrelImporterReport builds one importer row: the names it uses, the
// direct-import suggestion, and (when every used name resolves to an origin
// file) the replacement edits for the importer's barrel imports.
func barrelImporterReport(ws *core.Workspace, importer *ast.SourceFile, barrel *ast.SourceFile, exports map[string]barrelExportOrigin) (*BarrelImporter, []DeadCodeFixEdit) {
	row := &BarrelImporter{File: ws.RelPath(importer.FileName()), UsedNames: []string{}}
	importDecls := refactorImportsResolvingTo(ws, importer, barrel.FileName())

	type usedImport struct {
		exported string // name exported by the barrel
		local    string // local binding in the importer
	}
	var used []usedImport
	for _, decl := range importDecls {
		clause := decl.AsImportDeclaration().ImportClause
		if clause != nil {
			if clause.AsImportClause().Name() != nil ||
				(clause.AsImportClause().NamedBindings != nil && clause.AsImportClause().NamedBindings.Kind == ast.KindNamespaceImport) {
				row.Note = "uses a default or namespace import of the barrel; cannot suggest direct imports"
				return row, nil
			}
		}
		for _, spec := range refactorNamedImportSpecifiers(decl) {
			used = append(used, usedImport{exported: refactorImportedName(spec), local: spec.Name().Text()})
			row.UsedNames = append(row.UsedNames, refactorImportedName(spec))
		}
	}
	slices.Sort(row.UsedNames)
	row.UsedNames = slices.Compact(row.UsedNames)
	if len(used) == 0 {
		row.Note = "imports the barrel for side effects only; no direct-import suggestion"
		return row, nil
	}

	// Group used names by origin file, preserving aliasing: the direct import
	// binds the origin's source name to the importer's existing local name.
	byOrigin := make(map[string][]string) // origin file name -> import clauses
	originOrder := []string{}
	allResolved := true
	for _, u := range used {
		origin, ok := exports[u.exported]
		if !ok || origin.file == nil {
			allResolved = false
			continue
		}
		clauseText := origin.sourceName
		if origin.sourceName != u.local {
			clauseText = origin.sourceName + " as " + u.local
		}
		key := origin.file.FileName()
		if _, seen := byOrigin[key]; !seen {
			originOrder = append(originOrder, key)
		}
		byOrigin[key] = append(byOrigin[key], clauseText)
	}
	slices.Sort(originOrder)

	var lines []string
	for _, origin := range originOrder {
		specifier := refactorModuleSpecifierText(ws, importer.FileName(), origin)
		lines = append(lines, fmt.Sprintf("import { %s } from \"%s\";", strings.Join(byOrigin[origin], ", "), specifier))
	}
	row.Suggestion = strings.Join(lines, "\n")
	if !allResolved {
		row.Note = "some names could not be traced to an origin file; fix-plan skips this importer"
		return row, nil
	}
	if row.Suggestion == "" {
		return row, nil
	}

	// Replace the first barrel import statement with the direct imports and
	// delete the rest.
	var edits []DeadCodeFixEdit
	for i, decl := range importDecls {
		rng := refactorDeletionRange(importer, decl)
		newText := ""
		if i == 0 {
			newText = row.Suggestion + "\n"
		}
		edits = append(edits, DeadCodeFixEdit{Start: rng.Pos(), End: rng.End(), NewText: newText})
	}
	return row, edits
}

// writeJSONSection appends a labeled, indented JSON blob to text output.
func writeJSONSection(w io.Writer, label string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s:\n%s\n", label, encoded)
	return err
}
