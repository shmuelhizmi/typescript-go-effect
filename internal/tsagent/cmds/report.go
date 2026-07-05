package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// The `report` family renders multi-page, self-contained HTML measurement
// reports. A report is composed of providers; each provider turns the shared
// workspace into one or more report pages. Providers self-register from init()
// (see report_perf.go, report_quality.go, report_structure.go) so the family is
// extended by adding a file, never by editing this one.

// reportProvider turns the shared workspace into report pages.
type reportProvider struct {
	Key       string // selector for --include, e.g. "perf", "duplicates", "file-size"
	Group     string // sidebar group + preset selector: "Performance" | "Quality" | "Structure"
	Expensive bool   // dead-code, churn-risk: opt-in only (project-wide find-all-references / git)
	Build     func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error)
}

// reportProviders is the canonical, ordered provider list. Order here is the
// order pages appear in the sidebar.
var reportProviders []reportProvider

func registerProvider(p reportProvider) { reportProviders = append(reportProviders, p) }

// reportOptions carries the flags shared by every report command. Per-analysis
// thresholds use each producer's own defaults; only the perf knobs and the
// selection flags are surfaced here.
type reportOptions struct {
	out              string
	includeLibs      bool // perf: fold bundled libs/node_modules into rankings
	emit             bool // perf: measure the emit phase
	singleThreaded   bool // perf: single-threaded capture
	parallelPerf     bool // perf: opt into parallel capture
	top              int  // max rows per ranking table
	include          string
	includeExpensive bool
}

func registerReportFlags(fs *flag.FlagSet, generic bool) *reportOptions {
	o := &reportOptions{}
	fs.StringVar(&o.out, "out", "", "HTML output path (default tsagent-report.html)")
	fs.IntVar(&o.top, "top", 25, "max rows per ranking table (0 = all)")
	fs.BoolVar(&o.includeLibs, "include-libs", false, "include bundled libs/node_modules in the perf rankings")
	fs.BoolVar(&o.emit, "emit", false, "measure the emit phase in the perf capture")
	fs.BoolVar(&o.singleThreaded, "single-threaded", false, "single-threaded perf capture for cleaner per-file timing")
	fs.BoolVar(&o.parallelPerf, "parallel-perf", false, "run perf capture with parallel checker workers (higher memory)")
	if generic {
		fs.StringVar(&o.include, "include", "", "comma-separated report items (e.g. perf,duplicates,file-size)")
	} else {
		fs.BoolVar(&o.includeExpensive, "include-expensive", false, "include expensive analyses (dead-code, churn-risk)")
	}
	return o
}

func (o *reportOptions) ConfigOnlyWorkspace() bool {
	keys := splitCSV(o.include)
	return len(keys) == 1 && keys[0] == "perf"
}

func init() {
	register := func(name, summary string, generic bool, configOnly bool, sel func(o reportOptions) ([]reportProvider, error)) {
		cli.Register(cli.Command{
			Family:       "report",
			Name:         name,
			Summary:      summary,
			NeedsProgram: true,
			ConfigOnly:   configOnly,
			Flags: func(fs *flag.FlagSet) any {
				return registerReportFlags(fs, generic)
			},
			Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
				o := *flags.(*reportOptions)
				provs, err := sel(o)
				if err != nil {
					return nil, err
				}
				return runReport(ctx, ws, provs, o)
			},
		})
	}

	register("perf", "Multi-page HTML performance report (type-system budget, hot files/types, treemap)", false,
		true,
		func(o reportOptions) ([]reportProvider, error) { return providersInGroup("Performance", o), nil })
	register("quality", "Multi-page HTML quality report (duplicates, complexity, assertions, …)", false,
		false,
		func(o reportOptions) ([]reportProvider, error) { return providersInGroup("Quality", o), nil })
	register("structure", "Multi-page HTML structure report (file size, comment density, function metrics)", false,
		false,
		func(o reportOptions) ([]reportProvider, error) { return providersInGroup("Structure", o), nil })
	register("full", "Multi-page HTML report across every group (perf + quality + structure)", false,
		false,
		func(o reportOptions) ([]reportProvider, error) { return providersInGroup("", o), nil })
	register("", "Generic HTML report over an explicit --include set of items", true,
		false,
		func(o reportOptions) ([]reportProvider, error) { return providersFromInclude(o.include) })
}

// providersInGroup selects providers in the given group ("" = all groups),
// skipping Expensive ones unless --include-expensive was set.
func providersInGroup(group string, o reportOptions) []reportProvider {
	var out []reportProvider
	for _, p := range reportProviders {
		if group != "" && p.Group != group {
			continue
		}
		if p.Expensive && !o.includeExpensive {
			continue
		}
		out = append(out, p)
	}
	return out
}

// providersFromInclude resolves an explicit comma-separated key list to
// providers in canonical order. Naming an expensive key includes it. Unknown
// keys are a usage error.
func providersFromInclude(include string) ([]reportProvider, error) {
	keys := splitCSV(include)
	if len(keys) == 0 {
		return nil, cli.UsageErrorf("report: --include requires at least one item (e.g. --include perf,duplicates); known items: %s",
			strings.Join(providerKeys(), ", "))
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []reportProvider
	for _, p := range reportProviders {
		if want[p.Key] {
			out = append(out, p)
			delete(want, p.Key)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for k := range want {
			unknown = append(unknown, k)
		}
		slices.Sort(unknown)
		return nil, cli.UsageErrorf("report: unknown item(s) %s; known items: %s",
			strings.Join(unknown, ", "), strings.Join(providerKeys(), ", "))
	}
	return out, nil
}

func providerKeys() []string {
	keys := make([]string, len(reportProviders))
	for i, p := range reportProviders {
		keys[i] = p.Key
	}
	return keys
}

// ReportProviderKeys returns the registered report item keys in canonical
// order. Exported for shell completion of `report --include`.
func ReportProviderKeys() []string { return providerKeys() }

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// runReport builds the document from the selected providers and writes the HTML
// file. Per-provider failures are non-fatal: the report is still written with
// the pages that succeeded, and the command exits ExitPartial.
func runReport(ctx context.Context, ws *core.Workspace, provs []reportProvider, o reportOptions) (any, error) {
	if len(provs) == 0 {
		return nil, cli.UsageErrorf("report: no items selected")
	}
	doc := &report.Document{Title: "tsagent report", Brand: ws.RelPath(ws.RootDir)}
	pages, failures := buildReportPages(ctx, ws, provs, o)
	doc.Pages = append(doc.Pages, pages...)

	out := o.out
	if out == "" {
		out = "tsagent-report.html"
	}
	abs := tspath.GetNormalizedAbsolutePath(out, ws.Cwd)
	if err := ws.FS.WriteFile(abs, report.Render(doc)); err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "write HTML report: %v", err)
	}

	res := &ReportDocResult{Path: ws.RelPath(abs)}
	for _, p := range doc.Pages {
		res.Pages = append(res.Pages, ReportPageMeta{ID: p.ID, Label: p.Label, Group: p.Group, Badge: p.Badge})
	}
	if len(failures) > 0 {
		return res, cli.PartialErrorf("report: %d section(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return res, nil
}

type builtReportPages struct {
	index int
	pages []*report.Page
}

func buildReportPages(ctx context.Context, ws *core.Workspace, provs []reportProvider, o reportOptions) ([]*report.Page, []string) {
	perfIndex := slices.IndexFunc(provs, func(p reportProvider) bool { return p.Key == "perf" })
	if perfIndex < 0 {
		return buildProviders(ctx, ws, provs, o)
	}

	var built []builtReportPages
	var failures []string
	for i, p := range provs {
		if i == perfIndex {
			continue
		}
		pages, errs := buildProviders(ctx, ws, []reportProvider{p}, o)
		built = append(built, builtReportPages{index: i, pages: pages})
		failures = append(failures, errs...)
	}

	releaseWorkspaceProgram(ws)
	pages, errs := buildProviders(ctx, ws, []reportProvider{provs[perfIndex]}, o)
	built = append(built, builtReportPages{index: perfIndex, pages: pages})
	failures = append(failures, errs...)

	slices.SortFunc(built, func(a, b builtReportPages) int { return a.index - b.index })
	var out []*report.Page
	for _, b := range built {
		out = append(out, b.pages...)
	}
	return out, failures
}

func buildProviders(ctx context.Context, ws *core.Workspace, provs []reportProvider, o reportOptions) ([]*report.Page, []string) {
	var pages []*report.Page
	var failures []string
	for _, p := range provs {
		pagesForProvider, err := p.Build(ctx, ws, o)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Key, err))
			continue
		}
		pages = append(pages, pagesForProvider...)
	}
	return pages, failures
}

func releaseWorkspaceProgram(ws *core.Workspace) {
	if ws == nil || !ws.Releasable {
		return
	}
	ws.Program = nil
	ws.LS = nil
	ws.Conv = nil
	runtime.GC()
	runtime.GC()
}

// ReportDocResult is the result of a `report` command: the written HTML path
// and the page manifest.
type ReportDocResult struct {
	Path  string           `json:"reportPath"`
	Pages []ReportPageMeta `json:"pages"`
}

// ReportPageMeta describes one page in the written report.
type ReportPageMeta struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"`
	Badge string `json:"badge,omitempty"`
}

var _ cli.Texter = (*ReportDocResult)(nil)

func (r *ReportDocResult) WriteText(w io.Writer) error {
	for _, p := range r.Pages {
		badge := ""
		if p.Badge != "" {
			badge = "  (" + p.Badge + ")"
		}
		group := p.Group
		if group == "" {
			group = "-"
		}
		if _, err := fmt.Fprintf(w, "%-12s %s%s\n", group, p.Label, badge); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "wrote HTML report: %s (%d page(s))\n", r.Path, len(r.Pages))
	return err
}

// ---- shared page-builder helpers ----

// badgeCount renders a count as a sidebar badge, "" when zero.
func badgeCount(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// fmt1 renders a float with one decimal (treemap tooltips, table cells).
func fmt1(x float64) string { return fmt.Sprintf("%.1f", x) }
