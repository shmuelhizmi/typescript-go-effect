package cmds

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
)

func buildReportDoc(t *testing.T, ws *core.Workspace, provs []reportProvider, o reportOptions) *report.Document {
	t.Helper()
	doc := &report.Document{Title: "tsagent report"}
	for _, p := range provs {
		pages, err := p.Build(context.Background(), ws, o)
		if err != nil {
			t.Fatalf("provider %s: %v", p.Key, err)
		}
		doc.Pages = append(doc.Pages, pages...)
	}
	return doc
}

func TestReportIncludeUnknownIsUsageError(t *testing.T) {
	_, err := providersFromInclude("perf,bogus")
	if err == nil {
		t.Fatal("expected an error for an unknown item")
	}
	if code := cli.ExitCode(err); code != cli.ExitUsage {
		t.Fatalf("unknown item exit code = %d, want %d", code, cli.ExitUsage)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should name the unknown item: %v", err)
	}
}

func TestReportIncludePreservesCanonicalOrder(t *testing.T) {
	// Requested out of order; result must follow registry order.
	provs, err := providersFromInclude("duplicates,perf")
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 2 || provs[0].Key != "perf" || provs[1].Key != "duplicates" {
		t.Fatalf("got %v, want [perf duplicates]", keysOf(provs))
	}
}

func TestReportExpensiveGating(t *testing.T) {
	cheap := providersInGroup("", reportOptions{})
	for _, p := range cheap {
		if p.Expensive {
			t.Fatalf("expensive provider %s leaked into the default full set", p.Key)
		}
	}
	withExpensive := providersInGroup("", reportOptions{includeExpensive: true})
	if len(withExpensive) <= len(cheap) {
		t.Fatalf("--include-expensive (%d) should add providers over the default (%d)", len(withExpensive), len(cheap))
	}
	if !containsKey(withExpensive, "dead-code") || !containsKey(withExpensive, "churn-risk") {
		t.Fatal("--include-expensive should include dead-code and churn-risk")
	}
	// Naming an expensive key explicitly includes it even without the flag.
	provs, err := providersFromInclude("dead-code")
	if err != nil || !containsKey(provs, "dead-code") {
		t.Fatalf("explicit expensive include failed: %v", err)
	}
}

func TestReportPerfCommandUsesConfigOnlyWorkspace(t *testing.T) {
	cmd, ok := cli.Lookup("report", "perf")
	if !ok {
		t.Fatal("report perf command not registered")
	}
	if !cmd.NeedsProgram || !cmd.ConfigOnly {
		t.Fatalf("report perf NeedsProgram/ConfigOnly = %v/%v, want true/true", cmd.NeedsProgram, cmd.ConfigOnly)
	}
}

func TestPerfCommandsUseConfigOnlyWorkspace(t *testing.T) {
	for _, name := range []string{"summary", "hot-files", "hot-types", "hot-checks", "depth-limits"} {
		cmd, ok := cli.Lookup("perf", name)
		if !ok {
			t.Fatalf("perf %s command not registered", name)
		}
		if !cmd.NeedsProgram || !cmd.ConfigOnly {
			t.Fatalf("perf %s NeedsProgram/ConfigOnly = %v/%v, want true/true", name, cmd.NeedsProgram, cmd.ConfigOnly)
		}
	}
}

func TestPerfCaptureFlagsDefaultToLowMemory(t *testing.T) {
	if !((&captureFlags{}).options().SingleThreaded) {
		t.Fatal("perf capture should default to single-threaded")
	}
	if (&captureFlags{parallelPerf: true}).options().SingleThreaded {
		t.Fatal("--parallel-perf should opt into parallel capture")
	}
	if !((&captureFlags{parallelPerf: true, singleThreaded: true}).options().SingleThreaded) {
		t.Fatal("--single-threaded should win over --parallel-perf")
	}
}

func TestGenericReportPerfIncludeUsesConfigOnlyWorkspace(t *testing.T) {
	if !((&reportOptions{include: "perf"}).ConfigOnlyWorkspace()) {
		t.Fatal("generic report --include perf should use a config-only workspace")
	}
	for _, include := range []string{"", "perf,duplicates", "duplicates", "bogus"} {
		if (&reportOptions{include: include}).ConfigOnlyWorkspace() {
			t.Fatalf("include %q should not use a config-only workspace", include)
		}
	}
}

func TestReportStructureMeasurements(t *testing.T) {
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "// header line one\n// header line two\n" +
			"export function f(a: number, b: number, c: number) {\n" +
			"  if (a) { if (b) { if (c) { return 1 } } }\n" +
			"  return 0\n}\n",
		"/project/src/b.ts": "export const x = 1;\n",
	})

	provs := providersInGroup("Structure", reportOptions{top: 25})
	doc := buildReportDoc(t, ws, provs, reportOptions{top: 25})
	html := report.Render(doc)

	for _, want := range []string{
		"File size", "Comment density", "Function metrics", "Directories",
		"src/a.ts", "src/b.ts",
		`id="treemap-file-size-1"`, `id="treemap-comments-1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("structure report missing %q", want)
		}
	}
	// One page per structure provider (file-size, comments, functions, dir-stats).
	if got := strings.Count(html, `class="page`); got != len(provs) {
		t.Errorf("rendered %d pages, want %d", got, len(provs))
	}
	// The treemap library is inlined exactly once across multiple treemaps.
	if n := strings.Count(html, "function tsRenderTreemap(rootId"); n != 1 {
		t.Errorf("treemap library inlined %d times, want 1", n)
	}
}

func TestReportRunWritesFileAndManifest(t *testing.T) {
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/a.ts": "// a\nexport const x = 1;\n",
	})
	provs := providersInGroup("Structure", reportOptions{top: 25})
	res, err := runReport(context.Background(), ws, provs, reportOptions{out: "out.html", top: 25})
	if err != nil {
		t.Fatalf("runReport: %v", err)
	}
	doc := res.(*ReportDocResult)
	if len(doc.Pages) != len(provs) {
		t.Fatalf("manifest has %d pages, want %d", len(doc.Pages), len(provs))
	}
	// The file was actually written and is a full HTML document.
	written, ok := ws.FS.ReadFile(ws.AbsPath("out.html"))
	if !ok {
		t.Fatal("report file was not written")
	}
	if !strings.Contains(written, "<!DOCTYPE html>") {
		t.Fatal("written file is not an HTML document")
	}
}

func TestReportBuildsPerfLastButPreservesPageOrder(t *testing.T) {
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const x = 1;\n"})
	var buildOrder []string
	provs := []reportProvider{
		{
			Key: "perf",
			Build: func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error) {
				buildOrder = append(buildOrder, "perf")
				if ws.Program != nil || ws.LS != nil || ws.Conv != nil {
					t.Fatal("perf provider should run after releasing the workspace program")
				}
				return []*report.Page{{ID: "perf", Label: "Performance"}}, nil
			},
		},
		{
			Key: "file-size",
			Build: func(ctx context.Context, ws *core.Workspace, o reportOptions) ([]*report.Page, error) {
				buildOrder = append(buildOrder, "file-size")
				if ws.Program == nil {
					t.Fatal("non-perf provider should run before releasing the workspace program")
				}
				return []*report.Page{{ID: "file-size", Label: "File size"}}, nil
			},
		},
	}

	pages, failures := buildReportPages(context.Background(), ws, provs, reportOptions{})
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}
	if got := strings.Join(buildOrder, ","); got != "file-size,perf" {
		t.Fatalf("build order = %s, want file-size,perf", got)
	}
	if len(pages) != 2 || pages[0].ID != "perf" || pages[1].ID != "file-size" {
		t.Fatalf("page order = %v, want [perf file-size]", pageIDs(pages))
	}
}

func TestReleaseWorkspaceProgramSkipsPersistentWorkspace(t *testing.T) {
	ws := newTestWorkspace(t, map[string]any{"/project/src/a.ts": "export const x = 1;\n"})
	ws.Releasable = false
	releaseWorkspaceProgram(ws)
	if ws.Program == nil || ws.LS == nil || ws.Conv == nil {
		t.Fatal("persistent workspace should not be released")
	}

	ws.Releasable = true
	releaseWorkspaceProgram(ws)
	if ws.Program != nil || ws.LS != nil || ws.Conv != nil {
		t.Fatal("releasable workspace should drop program, language service, and converters")
	}
}

func keysOf(provs []reportProvider) []string {
	out := make([]string, len(provs))
	for i, p := range provs {
		out[i] = p.Key
	}
	return out
}

func containsKey(provs []reportProvider, key string) bool {
	for _, p := range provs {
		if p.Key == key {
			return true
		}
	}
	return false
}

func pageIDs(pages []*report.Page) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.ID
	}
	return out
}
