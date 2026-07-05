package cmds

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
)

// qualityProvider registers a single-page provider in the Quality group.
func qualityProvider(key string, expensive bool, build func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error)) {
	registerProvider(reportProvider{
		Key:       key,
		Group:     "Quality",
		Expensive: expensive,
		Build: func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error) {
			p, err := build(ctx, ws, o)
			if err != nil {
				return nil, err
			}
			return []*report.Page{p}, nil
		},
	})
}

func init() {
	qualityProvider("duplicates", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeDuplicates(ctx, ws, &duplicatesFlags{minNodes: 25}, nil)
		if err != nil {
			return nil, err
		}
		maxScore := 0.0
		for _, c := range res.Classes {
			if sc := float64(len(c.Members) * c.NodeCount); sc > maxScore {
				maxScore = sc
			}
		}
		classes := topNSlice(res.Classes, o.top)
		rows := make([]report.Row, 0, len(classes))
		for _, c := range classes {
			rows = append(rows, report.Row{
				{Text: strconv.Itoa(len(c.Members)), Class: "r", Meter: &report.MeterSpec{Val: float64(len(c.Members) * c.NodeCount), Max: maxScore, Color: "var(--c-accent2)"}},
				{Text: strconv.Itoa(c.NodeCount), Class: "r mono dim"},
				{Text: cloneLocations(c.Members), Class: "path"},
			})
		}
		return &report.Page{
			ID: "duplicates", Label: "Duplicates", Group: "Quality", Badge: badgeCount(len(res.Classes)),
			Body: []report.Section{report.Table("Clone classes", "No structural clones found.", []report.Col{
				{Header: "Members", Right: true}, {Header: "Nodes", Right: true}, {Header: "Locations"},
			}, rows)},
		}, nil
	})

	qualityProvider("complexity", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeComplexity(ctx, ws, &analyzeComplexityFlags{top: o.top, typeCandidateLimit: reportComplexityTypeCandidateLimit(o.top)}, nil)
		if err != nil {
			return nil, err
		}
		maxScore := 0.0
		for _, e := range res.Entries {
			if e.Score > maxScore {
				maxScore = e.Score
			}
		}
		rows := make([]report.Row, 0, len(res.Entries))
		for _, e := range res.Entries {
			rows = append(rows, report.Row{
				{Text: e.Name, Class: "sym"},
				{Text: fmt1(e.Score), Class: "r", Meter: &report.MeterSpec{Val: e.Score, Max: maxScore, Color: "var(--c-hot)"}},
				{Text: strconv.Itoa(e.Cyclomatic), Class: "r mono dim"},
				{Text: strconv.Itoa(e.Cognitive), Class: "r mono dim"},
				{Text: strconv.Itoa(e.TypeComplexity), Class: "r mono dim"},
				{Text: fmt.Sprintf("%s:%d", e.File, e.Line), Class: "path dim"},
			})
		}
		return &report.Page{
			ID: "complexity", Label: "Complexity", Group: "Quality", Badge: badgeCount(len(res.Entries)),
			Body: []report.Section{report.Table("Most complex functions", "No functions analyzed.", []report.Col{
				{Header: "Function"}, {Header: "Score", Right: true}, {Header: "Cyclomatic", Right: true},
				{Header: "Cognitive", Right: true}, {Header: "Type", Right: true}, {Header: "Location"},
			}, rows)},
		}, nil
	})

	qualityProvider("assertions", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeAssertionCounts(ws, nil)
		if err != nil {
			return nil, err
		}
		type fc struct {
			file  string
			total int
		}
		files := make([]fc, 0, len(res.Files))
		maxTotal := 0
		for _, f := range res.Files {
			total := 0
			for _, n := range f.Counts {
				total += n
			}
			files = append(files, fc{f.File, total})
			if total > maxTotal {
				maxTotal = total
			}
		}
		slices.SortFunc(files, func(a, b fc) int {
			if a.total != b.total {
				return b.total - a.total
			}
			return strings.Compare(a.file, b.file)
		})
		topFiles := topNSlice(files, o.top)
		rows := make([]report.Row, 0, len(topFiles))
		for _, f := range topFiles {
			rows = append(rows, report.Row{
				{Text: f.file, Class: "path"},
				{Text: strconv.Itoa(f.total), Class: "r", Meter: &report.MeterSpec{Val: float64(f.total), Max: float64(maxTotal), Color: "var(--c-accent)"}},
			})
		}
		return &report.Page{
			ID: "assertions", Label: "Assertions", Group: "Quality", Badge: badgeCount(totalKindCounts(res.Totals)),
			Body: []report.Section{report.Table("Assertion density (casts, non-null, satisfies, ts-* directives)", "No assertions found.", []report.Col{
				{Header: "File"}, {Header: "Assertions", Right: true},
			}, rows)},
		}, nil
	})

	qualityProvider("exhaustiveness", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeExhaustiveness(ctx, ws, &exhaustivenessFlags{}, nil)
		if err != nil {
			return nil, err
		}
		exhaustivenessRows := topNSlice(res.Rows, o.top)
		rows := make([]report.Row, 0, len(exhaustivenessRows))
		for _, r := range exhaustivenessRows {
			rows = append(rows, report.Row{
				{Text: fmt.Sprintf("%s:%d", r.File, r.Line), Class: "path"},
				{Text: r.Discriminant, Class: "mono"},
				{Text: strings.Join(r.Missing, ", ")},
				{Text: r.Status, Class: "dim"},
			})
		}
		return &report.Page{
			ID: "exhaustiveness", Label: "Exhaustiveness", Group: "Quality", Badge: badgeCount(len(res.Rows)),
			Body: []report.Section{report.Table("Non-exhaustive switches", "All switches over literal unions/enums are exhaustive.", []report.Col{
				{Header: "Switch"}, {Header: "Discriminant"}, {Header: "Missing variants"}, {Header: "Status"},
			}, rows)},
		}, nil
	})

	qualityProvider("barrel-cost", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeBarrelCost(ctx, ws, &barrelCostFlags{minReexports: 3}, nil)
		if err != nil {
			return nil, err
		}
		maxLines := 0
		for _, b := range res.Barrels {
			if b.TransitiveLines > maxLines {
				maxLines = b.TransitiveLines
			}
		}
		barrels := topNSlice(res.Barrels, o.top)
		rows := make([]report.Row, 0, len(barrels))
		for _, b := range barrels {
			rows = append(rows, report.Row{
				{Text: b.File, Class: "path"},
				{Text: strconv.Itoa(b.Reexports), Class: "r mono dim"},
				{Text: strconv.Itoa(b.TransitiveFiles), Class: "r mono dim"},
				{Text: strconv.Itoa(b.TransitiveLines), Class: "r", Meter: &report.MeterSpec{Val: float64(b.TransitiveLines), Max: float64(maxLines), Color: "var(--c-hot)"}},
				{Text: strconv.Itoa(len(b.Importers)), Class: "r mono dim"},
			})
		}
		return &report.Page{
			ID: "barrel-cost", Label: "Barrel cost", Group: "Quality", Badge: badgeCount(len(res.Barrels)),
			Body: []report.Section{report.Table("Barrel files and their transitive import cost", "No barrels above the re-export threshold.", []report.Col{
				{Header: "Barrel"}, {Header: "Re-exports", Right: true}, {Header: "Transitive files", Right: true},
				{Header: "Transitive lines", Right: true}, {Header: "Importers", Right: true},
			}, rows)},
		}, nil
	})

	qualityProvider("side-effects", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeSideEffects(ctx, ws, &sideEffectsFlags{module: true, depth: 3}, nil)
		if err != nil {
			return nil, err
		}
		maxEv := 0
		var impure []*ModulePurity
		for _, m := range res.Modules {
			if m.SafeToTreeShake {
				continue
			}
			impure = append(impure, m)
			if len(m.Evidence) > maxEv {
				maxEv = len(m.Evidence)
			}
		}
		topImpure := topNSlice(impure, o.top)
		rows := make([]report.Row, 0, len(topImpure))
		for _, m := range topImpure {
			rows = append(rows, report.Row{
				{Text: m.File, Class: "path"},
				{Text: strconv.Itoa(len(m.Evidence)), Class: "r", Meter: &report.MeterSpec{Val: float64(len(m.Evidence)), Max: float64(maxEv), Color: "var(--c-accent)"}},
			})
		}
		return &report.Page{
			ID: "side-effects", Label: "Side effects", Group: "Quality", Badge: badgeCount(len(impure)),
			Body: []report.Section{report.Table("Modules with top-level side effects (not tree-shake safe)", "All modules are tree-shake safe.", []report.Col{
				{Header: "Module"}, {Header: "Side-effect ops", Right: true},
			}, rows)},
		}, nil
	})

	qualityProvider("unused-deps", false, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeUnusedDeps(ctx, ws, &unusedDepsFlags{dev: true}, nil)
		if err != nil {
			return nil, err
		}
		rows := make([]report.Row, 0, len(res.UnusedDeps)+len(res.PhantomDeps))
		for _, d := range res.UnusedDeps {
			rows = append(rows, report.Row{{Text: d, Class: "mono"}, {Text: "unused (declared, never imported)", Class: "dim"}})
		}
		for _, d := range res.PhantomDeps {
			rows = append(rows, report.Row{{Text: d, Class: "mono"}, {Text: "phantom (imported, never declared)", Class: "dim"}})
		}
		totalRows := len(rows)
		rows = topNSlice(rows, o.top)
		empty := "All dependencies are accounted for."
		if res.Note != "" {
			empty = res.Note
		}
		return &report.Page{
			ID: "unused-deps", Label: "Dependencies", Group: "Quality", Badge: badgeCount(totalRows),
			Body: []report.Section{report.Table("Dependency hygiene", empty, []report.Col{
				{Header: "Dependency"}, {Header: "Status"},
			}, rows)},
		}, nil
	})

	qualityProvider("dead-code", true, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeDeadCode(ctx, ws, &deadCodeFlags{}, nil)
		if err != nil {
			return nil, err
		}
		symbols := topNSlice(res.Symbols, o.top)
		rows := make([]report.Row, 0, len(symbols))
		for _, s := range symbols {
			rows = append(rows, report.Row{
				{Text: s.Name, Class: "sym"},
				{Text: s.Kind, Class: "dim"},
				{Text: s.Confidence, Class: "dim"},
				{Text: fmt.Sprintf("%s:%d", s.File, s.Line), Class: "path dim"},
			})
		}
		return &report.Page{
			ID: "dead-code", Label: "Dead code", Group: "Quality", Badge: badgeCount(len(res.Symbols)),
			Body: []report.Section{report.Table("Unreferenced symbols", "No dead code found.", []report.Col{
				{Header: "Symbol"}, {Header: "Kind"}, {Header: "Confidence"}, {Header: "Location"},
			}, rows)},
		}, nil
	})

	qualityProvider("churn-risk", true, func(ctx context.Context, ws *core.Workspace, o reportOptions) (*report.Page, error) {
		res, err := runAnalyzeChurnRisk(ctx, ws, &churnRiskFlags{since: "6 months ago", top: o.top}, nil)
		if err != nil {
			return nil, err
		}
		maxRisk := 0.0
		for _, r := range res.Rows {
			if r.Risk > maxRisk {
				maxRisk = r.Risk
			}
		}
		rows := make([]report.Row, 0, len(res.Rows))
		for _, r := range res.Rows {
			rows = append(rows, report.Row{
				{Text: r.Name, Class: "sym"},
				{Text: fmt1(r.Risk), Class: "r", Meter: &report.MeterSpec{Val: r.Risk, Max: maxRisk, Color: "var(--c-hot)"}},
				{Text: strconv.Itoa(r.Churn), Class: "r mono dim"},
				{Text: strconv.Itoa(r.Refs), Class: "r mono dim"},
				{Text: r.File, Class: "path dim"},
			})
		}
		return &report.Page{
			ID: "churn-risk", Label: "Churn risk", Group: "Quality", Badge: badgeCount(len(res.Rows)),
			Body: []report.Section{report.Table("High-churn × high-reference symbols", "No churn data (needs git history).", []report.Col{
				{Header: "Symbol"}, {Header: "Risk", Right: true}, {Header: "Churn", Right: true},
				{Header: "Refs", Right: true}, {Header: "File"},
			}, rows)},
		}, nil
	})
}

func reportComplexityTypeCandidateLimit(top int) int {
	if top <= 0 {
		return 0
	}
	return max(top*10, 200)
}

func totalKindCounts(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// cloneLocations renders a compact location summary for a clone class.
func cloneLocations(members []*CloneMember) string {
	if len(members) == 0 {
		return ""
	}
	first := fmt.Sprintf("%s:%d-%d", members[0].File, members[0].StartLine, members[0].EndLine)
	if len(members) > 1 {
		return fmt.Sprintf("%s  +%d more", first, len(members)-1)
	}
	return first
}
