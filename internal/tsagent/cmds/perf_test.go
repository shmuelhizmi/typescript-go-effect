package cmds

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/perf"
	"github.com/microsoft/typescript-go/internal/tsagent/report"
)

func gatherFixture(t *testing.T) *perf.Capture {
	t.Helper()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/models.ts": `
export interface Animal { name: string }
export interface Dog extends Animal { bark(): void }
export class Box<T> { constructor(public value: T) {} }

export const a = new Box<Animal>({ name: "a" });
export const b = new Box<Dog>({ name: "b", bark() {} });
export const c = new Box<string>("c");
export const d = new Box<number>(1);
`,
		"/project/src/index.ts": `
import { Box } from "./models";
export const e = new Box<boolean>(true);
`,
	})
	c, err := perf.Gather(context.Background(), ws, perf.Options{SingleThreaded: true})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return c
}

func TestPerfSummary(t *testing.T) {
	c := gatherFixture(t)
	s := c.Summary()
	if s.Stats.Files == 0 {
		t.Fatal("expected non-zero file count")
	}
	if s.Stats.Types == 0 {
		t.Fatal("expected non-zero type count")
	}
	if s.Stats.Instantiations == 0 {
		t.Fatal("expected non-zero instantiation count")
	}
	if s.Verdict == "" {
		t.Fatal("expected a verdict")
	}
	// Phase percentages should be sane (sum ~100 across measured phases).
	total := s.ParsePct + s.BindPct + s.CheckPct + s.EmitPct
	if total < 90 || total > 110 {
		t.Fatalf("phase percentages sum to %.1f, want ~100", total)
	}
}

func TestPerfSummaryOnlySkipsRankingData(t *testing.T) {
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/index.ts": `
export interface Box<T> { value: T }
export const value: Box<string> = { value: "ok" };
`,
	})
	c, err := perf.Gather(context.Background(), ws, perf.Options{SingleThreaded: true, SummaryOnly: true})
	if err != nil {
		t.Fatalf("Gather summary-only: %v", err)
	}
	if c.Summary().Stats.Types == 0 {
		t.Fatal("summary-only capture should still collect compiler stats")
	}
	if got := c.HotFiles(false); len(got) != 0 {
		t.Fatalf("summary-only capture retained hot files: %v", paths(got))
	}
	if got := c.HotTypes(false); len(got) != 0 {
		t.Fatalf("summary-only capture retained hot types: %v", typeNames(got))
	}
	if got := c.HotChecks(); len(got) != 0 {
		t.Fatalf("summary-only capture retained hot checks: %#v", got)
	}
}

func TestPerfHotFilesScoping(t *testing.T) {
	c := gatherFixture(t)

	project := c.HotFiles(false)
	if len(project) == 0 {
		t.Fatal("expected project hot-files")
	}
	for _, f := range project {
		if strings.HasPrefix(f.Path, "bundled:") {
			t.Fatalf("project scope leaked a bundled lib: %s", f.Path)
		}
	}
	if !slices.ContainsFunc(project, func(f *perf.HotFile) bool {
		return strings.Contains(f.Path, "models.ts")
	}) {
		t.Fatalf("expected models.ts in project hot-files, got %v", paths(project))
	}

	// Ranking is sorted by total time descending.
	for i := 1; i < len(project); i++ {
		if project[i-1].TotalMS < project[i].TotalMS {
			t.Fatalf("hot-files not sorted descending at %d", i)
		}
	}

	withLibs := c.HotFiles(true)
	if !slices.ContainsFunc(withLibs, func(f *perf.HotFile) bool {
		return strings.HasPrefix(f.Path, "bundled:")
	}) {
		t.Fatal("expected bundled libs when --include-libs is set")
	}
	if len(withLibs) <= len(project) {
		t.Fatalf("include-libs (%d) should yield more files than project-only (%d)", len(withLibs), len(project))
	}
}

func TestPerfHotTypesSpread(t *testing.T) {
	c := gatherFixture(t)

	project := c.HotTypes(false)
	if len(project) == 0 {
		t.Fatal("expected project hot-types")
	}
	// The generic Box is instantiated several times; it should surface and be
	// attributed to models.ts.
	box := findType(project, "Box")
	if box == nil {
		t.Fatalf("expected Box in hot-types, got %v", typeNames(project))
	}
	if box.Count < 2 {
		t.Fatalf("expected Box instantiated multiple times, count=%d", box.Count)
	}
	if !strings.Contains(box.File, "models.ts") {
		t.Fatalf("expected Box attributed to models.ts, got %s", box.File)
	}
	// Project scope must not surface bundled-lib declarations.
	for _, ht := range project {
		if strings.HasPrefix(ht.File, "bundled:") {
			t.Fatalf("project scope leaked a bundled-lib type: %s", ht.Symbol)
		}
	}
}

func TestPerfDepthLimitsAndHotChecksEmptySafe(t *testing.T) {
	c := gatherFixture(t)
	// A small, healthy project trips no guards and has no slow sampled checks;
	// the analyzers must handle empty results without error.
	if dl := c.DepthLimits(); len(dl) != 0 {
		t.Logf("unexpected depth limits on a trivial project: %d (not fatal)", len(dl))
	}
	_ = c.HotChecks()
}

func TestPerfReport(t *testing.T) {
	c := gatherFixture(t)
	rep := c.Report(false, 10)
	if rep.Summary == nil {
		t.Fatal("report missing summary")
	}
	if len(rep.HotFiles) == 0 || len(rep.HotTypes) == 0 {
		t.Fatal("report missing hot-files/hot-types")
	}
	if !slices.ContainsFunc(rep.HotTypes, func(t *perf.HotType) bool { return t.Symbol == "Box" }) {
		t.Fatalf("report hot-types missing Box: %v", typeNames(rep.HotTypes))
	}

	htmlDoc := report.Render(&report.Document{Title: "tsagent report", Pages: perfPages(rep)})
	for _, want := range []string{
		"<!DOCTYPE html>", "</html>", "class=\"navlist\"", "class=\"page",
		rep.Summary.Verdict, "Box",
		"id=\"treemap-perf-1\"", "tsRenderTreemap(\"treemap-perf-1\"",
	} {
		if !strings.Contains(htmlDoc, want) {
			t.Fatalf("HTML report missing %q", want)
		}
	}
	// The page-switch nav must replace the old scroll-spy.
	if strings.Contains(htmlDoc, "IntersectionObserver") {
		t.Fatal("HTML report still uses the single-page scroll-spy")
	}
	if strings.Contains(htmlDoc, "persists") {
		t.Fatal("HTML report contains corrupted CSS token")
	}
}

func paths(files []*perf.HotFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func typeNames(types []*perf.HotType) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = t.Symbol
	}
	return out
}

func findType(types []*perf.HotType, name string) *perf.HotType {
	for _, t := range types {
		if t.Symbol == name {
			return t
		}
	}
	return nil
}
