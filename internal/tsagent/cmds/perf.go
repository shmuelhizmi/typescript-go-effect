package cmds

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tsagent/perf"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// captureFlags are shared by every perf command: they control the traced
// compile that gathers the data.
type captureFlags struct {
	emit           bool
	singleThreaded bool
	includeLibs    bool
	top            int
	generateReport bool
	out            string
}

func (f *captureFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.emit, "emit", false, "include the emit phase (measures declaration-emit cost)")
	fs.BoolVar(&f.singleThreaded, "single-threaded", false, "use one checker for cleaner per-file timing attribution")
}

// registerScoped adds --include-libs for the ranking commands, which default to
// the user's own files.
func (f *captureFlags) registerScoped(fs *flag.FlagSet) {
	f.register(fs)
	fs.BoolVar(&f.includeLibs, "include-libs", false, "include bundled lib and node_modules files in the ranking")
}

// registerReport adds the flags for the combined `perf report` command.
func (f *captureFlags) registerReport(fs *flag.FlagSet) {
	f.registerScoped(fs)
	fs.IntVar(&f.top, "top", 25, "max rows per ranking section (0 = all)")
	fs.BoolVar(&f.generateReport, "generate-report", false, "also write a standalone HTML report")
	fs.StringVar(&f.out, "out", "", "HTML report path (default tsagent-perf-report.html); implies --generate-report")
}

func (f *captureFlags) options() perf.Options {
	return perf.Options{Emit: f.emit, SingleThreaded: f.singleThreaded}
}

func capture(ctx context.Context, ws *core.Workspace, f *captureFlags) (*perf.Capture, error) {
	c, err := perf.Gather(ctx, ws, f.options())
	if err != nil {
		return nil, cli.Errorf(cli.ExitFailed, "perf capture failed: %v", err)
	}
	return c, nil
}

func init() {
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "summary",
		Summary:      "Project type-system performance budget: phase split, counters, and a verdict",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.register(fs)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			c, err := capture(ctx, ws, flags.(*captureFlags))
			if err != nil {
				return nil, err
			}
			return &SummaryResult{Summary: c.Summary()}, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "report",
		Summary:      "Full performance report (all analyses) with optional standalone HTML export",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.registerReport(fs)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			f := flags.(*captureFlags)
			c, err := capture(ctx, ws, f)
			if err != nil {
				return nil, err
			}
			res := &ReportResult{Report: c.Report(f.includeLibs, f.top)}
			if f.generateReport || f.out != "" {
				path := f.out
				if path == "" {
					path = "tsagent-perf-report.html"
				}
				abs := tspath.GetNormalizedAbsolutePath(path, ws.Cwd)
				if err := ws.FS.WriteFile(abs, perf.RenderHTML(res.Report)); err != nil {
					return nil, cli.Errorf(cli.ExitFailed, "write HTML report: %v", err)
				}
				res.ReportPath = ws.RelPath(abs)
			}
			return res, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "hot-files",
		Summary:      "Files ranked by compiler time (parse/bind/check) and recorded type count",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.registerScoped(fs)
			fs.IntVar(&f.top, "top", 50, "keep only the N hottest files (0 = all)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			f := flags.(*captureFlags)
			c, err := capture(ctx, ws, f)
			if err != nil {
				return nil, err
			}
			files := c.HotFiles(f.includeLibs)
			if f.top > 0 && len(files) > f.top {
				files = files[:f.top]
			}
			return &HotFilesResult{Files: files}, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "hot-types",
		Summary:      "Generics/aliases ranked by how many distinct types they instantiate (instantiation spread)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.registerScoped(fs)
			fs.IntVar(&f.top, "top", 50, "keep only the N most-instantiated symbols (0 = all)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			f := flags.(*captureFlags)
			c, err := capture(ctx, ws, f)
			if err != nil {
				return nil, err
			}
			types := c.HotTypes(f.includeLibs)
			if f.top > 0 && len(types) > f.top {
				types = types[:f.top]
			}
			return &HotTypesResult{Types: types}, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "hot-checks",
		Summary:      "Individual slow checker operations the tracer sampled (>~10ms), with source location",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.register(fs)
			fs.IntVar(&f.top, "top", 50, "keep only the N slowest operations (0 = all)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			f := flags.(*captureFlags)
			c, err := capture(ctx, ws, f)
			if err != nil {
				return nil, err
			}
			checks := c.HotChecks()
			if f.top > 0 && len(checks) > f.top {
				checks = checks[:f.top]
			}
			return &HotChecksResult{Checks: checks}, nil
		},
	})
	cli.Register(cli.Command{
		Family:       "perf",
		Name:         "depth-limits",
		Summary:      "Type explosions that tripped a depth/size guard — the highest-value fixes",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &captureFlags{}
			f.register(fs)
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			c, err := capture(ctx, ws, flags.(*captureFlags))
			if err != nil {
				return nil, err
			}
			return &DepthLimitsResult{Limits: c.DepthLimits()}, nil
		},
	})
}

// ---- summary result ----

// SummaryResult is the `perf summary` result.
type SummaryResult struct {
	Summary *perf.Summary `json:"summary"`
}

var _ cli.Texter = (*SummaryResult)(nil)

func (r *SummaryResult) WriteText(w io.Writer) error {
	s := r.Summary
	st := s.Stats
	if _, err := fmt.Fprintf(w, "Files %d  Lines %d  Symbols %d  Types %d  Instantiations %d  Mem %dK\n",
		st.Files, st.Lines, st.Symbols, st.Types, st.Instantiations, st.MemoryUsed/1024); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "parse %.3fs (%.0f%%)  bind %.3fs (%.0f%%)  check %.3fs (%.0f%%)  emit %.3fs (%.0f%%)  total %.3fs\n",
		st.Parse.Seconds(), s.ParsePct, st.Bind.Seconds(), s.BindPct,
		st.Check.Seconds(), s.CheckPct, st.Emit.Seconds(), s.EmitPct, st.Total.Seconds()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "types/file %.1f  instantiations/type %.2f  depth-limit hits %d\n",
		s.TypesPerFile, s.InstantiationsPerType, s.DepthLimitHits); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "verdict: %s\n", s.Verdict); err != nil {
		return err
	}
	for _, n := range s.Notes {
		if _, err := fmt.Fprintf(w, "  ! %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

// ---- full report result ----

// ReportResult is the `perf report` result: the full combined report, plus the
// path of any HTML file written.
type ReportResult struct {
	Report     *perf.Report `json:"report"`
	ReportPath string       `json:"reportPath,omitempty"`
}

var _ cli.Texter = (*ReportResult)(nil)

func (r *ReportResult) WriteText(w io.Writer) error {
	rep := r.Report
	if err := (&SummaryResult{Summary: rep.Summary}).WriteText(w); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "\n== depth-limit guards (%d) ==\n", len(rep.DepthLimits)); err != nil {
		return err
	}
	if len(rep.DepthLimits) == 0 {
		fmt.Fprintln(w, "  none — no pathological type explosions detected")
	}
	for _, d := range rep.DepthLimits {
		loc := d.File
		if loc == "" {
			loc = "(unattributed)"
		}
		if _, err := fmt.Fprintf(w, "  %s  %s  [%s]\n", d.Name, loc, d.Detail); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(w, "\n== hot files (%d) ==\n", len(rep.HotFiles)); err != nil {
		return err
	}
	for _, f := range rep.HotFiles {
		if _, err := fmt.Fprintf(w, "  %8.1fms  %s  (parse %.1f bind %.1f check %.1f, types %d)\n",
			f.TotalMS, f.Path, f.ParseMS, f.BindMS, f.CheckMS, f.Types); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(w, "\n== hot types (%d) ==\n", len(rep.HotTypes)); err != nil {
		return err
	}
	for _, t := range rep.HotTypes {
		loc := t.File
		if t.Line > 0 {
			loc = fmt.Sprintf("%s:%d", t.File, t.Line)
		}
		if _, err := fmt.Fprintf(w, "  %6d  %s  %s  (conditional:%d maxUnion:%d)\n",
			t.Count, t.Symbol, loc, t.Conditional, t.MaxUnion); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(w, "\n== hot checks (%d) ==\n", len(rep.HotChecks)); err != nil {
		return err
	}
	if len(rep.HotChecks) == 0 {
		fmt.Fprintln(w, "  none sampled — the type system is fast")
	}
	for _, c := range rep.HotChecks {
		loc := c.File
		if c.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.File, c.Line)
		}
		if _, err := fmt.Fprintf(w, "  %8.1fms  %s  %s\n", c.DurMS, c.Name, loc); err != nil {
			return err
		}
	}

	if r.ReportPath != "" {
		if _, err := fmt.Fprintf(w, "\nwrote HTML report: %s\n", r.ReportPath); err != nil {
			return err
		}
	}
	return nil
}

// ---- hot files result ----

// HotFilesResult is the `perf hot-files` result.
type HotFilesResult struct {
	Files []*perf.HotFile
}

var _ cli.Lister = (*HotFilesResult)(nil)

func (r *HotFilesResult) Total() int     { return len(r.Files) }
func (r *HotFilesResult) Item(i int) any { return r.Files[i] }

func (r *HotFilesResult) WriteItemText(w io.Writer, item any) error {
	f := item.(*perf.HotFile)
	_, err := fmt.Fprintf(w, "%8.1fms  %s  (parse %.1f bind %.1f check %.1f, types %d)\n",
		f.TotalMS, f.Path, f.ParseMS, f.BindMS, f.CheckMS, f.Types)
	return err
}

// ---- hot types result ----

// HotTypesResult is the `perf hot-types` result.
type HotTypesResult struct {
	Types []*perf.HotType
}

var _ cli.Lister = (*HotTypesResult)(nil)

func (r *HotTypesResult) Total() int     { return len(r.Types) }
func (r *HotTypesResult) Item(i int) any { return r.Types[i] }

func (r *HotTypesResult) WriteItemText(w io.Writer, item any) error {
	t := item.(*perf.HotType)
	loc := t.File
	if t.Line > 0 {
		loc = fmt.Sprintf("%s:%d", t.File, t.Line)
	}
	_, err := fmt.Fprintf(w, "%6d  %s  %s  (conditional:%d maxUnion:%d)\n",
		t.Count, t.Symbol, loc, t.Conditional, t.MaxUnion)
	return err
}

// ---- hot checks result ----

// HotChecksResult is the `perf hot-checks` result.
type HotChecksResult struct {
	Checks []*perf.HotCheck
}

var (
	_ cli.Lister     = (*HotChecksResult)(nil)
	_ cli.ZeroTexter = (*HotChecksResult)(nil)
)

func (r *HotChecksResult) Total() int     { return len(r.Checks) }
func (r *HotChecksResult) Item(i int) any { return r.Checks[i] }
func (r *HotChecksResult) ZeroText() string {
	return "no checker operation was slow enough to be sampled (>~10ms) — the type system is fast"
}

func (r *HotChecksResult) WriteItemText(w io.Writer, item any) error {
	c := item.(*perf.HotCheck)
	loc := c.File
	if c.Line > 0 {
		loc = fmt.Sprintf("%s:%d", c.File, c.Line)
	}
	_, err := fmt.Fprintf(w, "%8.1fms  %s  %s\n", c.DurMS, c.Name, loc)
	return err
}

// ---- depth limits result ----

// DepthLimitsResult is the `perf depth-limits` result.
type DepthLimitsResult struct {
	Limits []*perf.DepthLimit
}

var (
	_ cli.Lister     = (*DepthLimitsResult)(nil)
	_ cli.ZeroTexter = (*DepthLimitsResult)(nil)
)

func (r *DepthLimitsResult) Total() int     { return len(r.Limits) }
func (r *DepthLimitsResult) Item(i int) any { return r.Limits[i] }
func (r *DepthLimitsResult) ZeroText() string {
	return "no depth/size guards fired — no pathological type explosions detected"
}

func (r *DepthLimitsResult) WriteItemText(w io.Writer, item any) error {
	d := item.(*perf.DepthLimit)
	loc := d.File
	if loc == "" {
		loc = "(unattributed)"
	}
	_, err := fmt.Fprintf(w, "%s  %s  [%s]\n", d.Name, loc, d.Detail)
	return err
}
