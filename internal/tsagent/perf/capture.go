// Package perf produces agent-oriented performance insights about a
// TypeScript project's type system. It drives a fresh, fully traced compile of
// the project entirely in memory (no trace files touch disk), then aggregates
// the resulting Chrome trace events, recorded type descriptors, and compiler
// statistics into the rankings the `perf` command family reports.
package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/execute/tsc"
	"github.com/microsoft/typescript-go/internal/tracing"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// traceDir is the virtual directory the in-memory tracing sink writes to. It is
// never created on disk.
const traceDir = "/__tsagent_perf_trace__"

// Options controls what the traced compile measures.
type Options struct {
	// Emit runs the emit phase (including declaration emit) so its cost is
	// measured. Output is discarded; nothing is written to disk.
	Emit bool
	// SingleThreaded forces a single checker, giving cleaner per-file timing
	// attribution at the cost of wall-clock speed.
	SingleThreaded bool
}

// Stats holds the project-level compiler counters and phase timings.
type Stats struct {
	Files          int           `json:"files"`
	Lines          int           `json:"lines"`
	Identifiers    int           `json:"identifiers"`
	Symbols        int           `json:"symbols"`
	Types          int           `json:"types"`
	Instantiations int           `json:"instantiations"`
	MemoryUsed     uint64        `json:"memoryUsedBytes"`
	MemoryAllocs   uint64        `json:"memoryAllocs"`
	Parse          time.Duration `json:"-"`
	Bind           time.Duration `json:"-"`
	Check          time.Duration `json:"-"`
	Emit           time.Duration `json:"-"`
	Total          time.Duration `json:"-"`
}

// Span is a normalized trace event with a duration: either a begin/end pair or
// a sampled "X" event.
type Span struct {
	Phase   string
	Name    string
	DurUS   float64 // microseconds
	Path    string
	Pos     int
	End     int
	Kind    int
	Sampled bool
	Args    map[string]any
}

// Instant is a zero-duration trace marker, e.g. a depth-limit guard firing.
type Instant struct {
	Phase string
	Name  string
	Args  map[string]any
}

// Capture is the result of a traced compile: aggregated raw material the
// analysis layer turns into rankings.
type Capture struct {
	Stats    Stats
	Spans    []Span
	Instants []Instant
	Types    []tracing.TypeDescriptor
	TypeByID map[uint32]*tracing.TypeDescriptor

	ws       *core.Workspace
	program  *compiler.Program
	canonMap map[string]string // any path representation -> canonical FileName()
}

// Gather builds a fresh traced program from the workspace config, drives all
// phases to populate the trace, and reads the trace back from the in-memory
// sink. The workspace's own warm program is left untouched.
func Gather(ctx context.Context, ws *core.Workspace, opts Options) (*Capture, error) {
	traceFS := newMemFS()
	tr, err := tracing.StartTracing(traceFS, traceDir, ws.ConfigPath, false)
	if err != nil {
		return nil, fmt.Errorf("start tracing: %w", err)
	}

	host := compiler.NewCachedFSCompilerHost(ws.Cwd, ws.FS, bundled.LibPath(), &tsc.ExtendedConfigCache{}, nil)
	programOpts := compiler.ProgramOptions{
		Config:  ws.Config,
		Host:    host,
		Tracing: tr,
	}
	if opts.SingleThreaded {
		programOpts.SingleThreaded = tscore.TSTrue
	}

	start := time.Now()
	program := compiler.NewProgram(programOpts)
	files := program.SourceFiles()
	parseDur := time.Since(start)

	// BindSourceFiles emits the per-file "bindSourceFile" trace spans (and only
	// binds files not yet bound). Run it before the check loop so binding is
	// attributed to the bind phase rather than lazily triggered during check.
	bindStart := time.Now()
	program.BindSourceFiles()
	bindDur := time.Since(bindStart)

	checkStart := time.Now()
	for _, f := range files {
		program.GetSemanticDiagnostics(ctx, f)
	}
	checkDur := time.Since(checkStart)

	var emitDur time.Duration
	if opts.Emit {
		emitStart := time.Now()
		program.Emit(ctx, compiler.EmitOptions{
			WriteFile: func(string, string, *compiler.WriteFileData) error { return nil },
		})
		emitDur = time.Since(emitStart)
	}

	if err := tr.StopTracing(); err != nil {
		return nil, fmt.Errorf("stop tracing: %w", err)
	}

	var mem runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&mem)

	c := &Capture{
		ws:       ws,
		program:  program,
		TypeByID: map[uint32]*tracing.TypeDescriptor{},
		Stats: Stats{
			Files:          len(files),
			Lines:          program.LineCount(),
			Identifiers:    program.IdentifierCount(),
			Symbols:        program.SymbolCount(),
			Types:          program.TypeCount(),
			Instantiations: program.InstantiationCount(),
			MemoryUsed:     mem.Alloc,
			MemoryAllocs:   mem.Mallocs,
			Parse:          parseDur,
			Bind:           bindDur,
			Check:          checkDur,
			Emit:           emitDur,
			Total:          parseDur + bindDur + checkDur + emitDur,
		},
	}
	if err := c.parseTrace(traceFS); err != nil {
		return nil, err
	}
	return c, nil
}

type traceEnvelopeEvent struct {
	PH   string         `json:"ph"`
	Cat  string         `json:"cat"`
	TID  int            `json:"tid"`
	TS   float64        `json:"ts"`
	Name string         `json:"name"`
	Dur  *float64       `json:"dur"`
	Args map[string]any `json:"args"`
}

// parseTrace reads trace.json and the per-checker types_*.json back from the
// in-memory sink and normalizes them into Spans, Instants, and Types.
func (c *Capture) parseTrace(fs *memFS) error {
	raw, ok := fs.ReadFile(traceDir + "/trace.json")
	if !ok {
		return fmt.Errorf("perf: trace output missing")
	}
	var events []traceEnvelopeEvent
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		return fmt.Errorf("perf: parse trace: %w", err)
	}

	// Pair begin/end events per thread (LIFO by name) into durations.
	type openEvent struct {
		name string
		ts   float64
		ev   traceEnvelopeEvent
	}
	stacks := map[int][]openEvent{}
	for _, ev := range events {
		switch ev.PH {
		case "B":
			stacks[ev.TID] = append(stacks[ev.TID], openEvent{name: ev.Name, ts: ev.TS, ev: ev})
		case "E":
			stack := stacks[ev.TID]
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == ev.Name {
					begin := stack[i]
					stacks[ev.TID] = append(stack[:i], stack[i+1:]...)
					c.Spans = append(c.Spans, spanFrom(begin.ev, ev.TS-begin.ts, false))
					break
				}
			}
		case "X":
			if ev.Dur != nil {
				c.Spans = append(c.Spans, spanFrom(ev, *ev.Dur, true))
			}
		case "I":
			c.Instants = append(c.Instants, Instant{Phase: ev.Cat, Name: ev.Name, Args: ev.Args})
		}
	}

	// Read recorded type descriptors from every checker's types_N.json. The
	// legend lists them; fall back to a key scan if absent.
	var typePaths []string
	if legend, ok := fs.ReadFile(traceDir + "/legend.json"); ok {
		var records []tracing.TraceRecord
		if json.Unmarshal([]byte(legend), &records) == nil {
			for _, r := range records {
				if r.TypesPath != "" {
					typePaths = append(typePaths, r.TypesPath)
				}
			}
		}
	}
	for _, p := range typePaths {
		data, ok := fs.ReadFile(p)
		if !ok {
			continue
		}
		var descs []tracing.TypeDescriptor
		if err := json.Unmarshal([]byte(data), &descs); err != nil {
			continue
		}
		c.Types = append(c.Types, descs...)
	}
	for i := range c.Types {
		c.TypeByID[c.Types[i].ID] = &c.Types[i]
	}
	return nil
}

func spanFrom(ev traceEnvelopeEvent, durUS float64, sampled bool) Span {
	s := Span{Phase: ev.Cat, Name: ev.Name, DurUS: durUS, Sampled: sampled, Args: ev.Args}
	if ev.Args != nil {
		if p, ok := ev.Args["path"].(string); ok {
			s.Path = p
		}
		s.Pos = argInt(ev.Args, "pos")
		s.End = argInt(ev.Args, "end")
		s.Kind = argInt(ev.Args, "kind")
	}
	return s
}

func argInt(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
