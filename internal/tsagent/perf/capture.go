// Package perf produces agent-oriented performance insights about a
// TypeScript project's type system. It drives a fresh, fully traced compile of
// the project, then aggregates the resulting Chrome trace events, recorded type
// descriptors, and compiler statistics into the rankings the `perf` command
// family reports.
package perf

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/execute/tsc"
	"github.com/microsoft/typescript-go/internal/json"
	"github.com/microsoft/typescript-go/internal/tracing"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs"
)

// traceDir is the trace root path exposed through the trace sink.
const traceDir = "/__tsagent_perf_trace__"

// Options controls what the traced compile measures.
type Options struct {
	// Emit runs the emit phase (including declaration emit) so its cost is
	// measured. Output is discarded; nothing is written to disk.
	Emit bool
	// SingleThreaded forces a single checker, giving cleaner per-file timing
	// attribution at the cost of wall-clock speed.
	SingleThreaded bool
	// IncludeLibs keeps all type-origin aggregations. Default captures only keep
	// project-owned hot type groups, matching the default reports.
	IncludeLibs bool
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
	Sampled bool
	Args    *traceArgs
}

// Instant is a zero-duration trace marker, e.g. a depth-limit guard firing.
type Instant struct {
	Phase string
	Name  string
	Args  traceArgs
}

// Capture is the result of a traced compile: aggregated raw material the
// analysis layer turns into rankings.
type Capture struct {
	Stats    Stats
	Spans    []Span
	Instants []Instant

	typeCounts      map[string]int // canonical file path -> recorded type count
	typeOriginIDs   map[uint32]struct{}
	typeOrigins     map[uint32]typeOrigin
	hotTypesAll     map[string]*hotTypeAgg
	hotTypesProject map[string]*hotTypeAgg

	ws         *core.Workspace
	program    *compiler.Program
	canonMap   map[string]string // any path representation -> canonical FileName()
	lineStarts map[string]tscore.ECMALineStarts
}

type typeOrigin struct {
	canon string
	line  int
}

type hotTypeAgg struct {
	ht         *HotType
	haveOrigin bool
}

// Gather builds a fresh traced program from the workspace config, drives all
// phases to populate the trace, and reads the trace back from the trace sink.
// The workspace's own warm program is left untouched.
func Gather(ctx context.Context, ws *core.Workspace, opts Options) (*Capture, error) {
	traceFS, err := newDiskTraceFS()
	if err != nil {
		return nil, fmt.Errorf("create trace sink: %w", err)
	}
	defer traceFS.Cleanup()
	tr, err := tracing.StartTracingWithOptions(traceFS, traceDir, ws.ConfigPath, false, tracing.Options{
		IncludeTypeDisplay:    false,
		StreamTypeDescriptors: true,
	})
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
		if err := tr.FlushTypeDescriptors(); err != nil {
			return nil, fmt.Errorf("flush tracing types: %w", err)
		}
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
	tr = nil

	var mem runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&mem)

	var hotTypesAll map[string]*hotTypeAgg
	if opts.IncludeLibs {
		hotTypesAll = map[string]*hotTypeAgg{}
	}
	c := &Capture{
		ws:              ws,
		program:         program,
		typeCounts:      map[string]int{},
		typeOriginIDs:   map[uint32]struct{}{},
		typeOrigins:     map[uint32]typeOrigin{},
		hotTypesAll:     hotTypesAll,
		hotTypesProject: map[string]*hotTypeAgg{},
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
	c.initCanonMap()
	c.program = nil
	program = nil
	files = nil
	host = nil
	programOpts.Host = nil
	programOpts.Tracing = nil
	runtime.GC()
	runtime.GC()
	if err := c.parseTraceEvents(traceFS); err != nil {
		return nil, err
	}
	runtime.GC()
	runtime.GC()
	if err := c.parseTraceTypes(traceFS); err != nil {
		return nil, err
	}
	c.typeOriginIDs = nil
	return c, nil
}

type traceEnvelopeEvent struct {
	PH   string    `json:"ph"`
	Cat  string    `json:"cat"`
	TID  int       `json:"tid"`
	TS   float64   `json:"ts"`
	Name string    `json:"name"`
	Dur  *float64  `json:"dur"`
	Args traceArgs `json:"args"`
}

type traceArgs struct {
	Path               string `json:"path,omitzero"`
	Pos                int    `json:"pos,omitzero"`
	End                int    `json:"end,omitzero"`
	Kind               int    `json:"kind,omitzero"`
	TypeID             uint32 `json:"typeId,omitzero"`
	SourceID           uint32 `json:"sourceId,omitzero"`
	TargetID           uint32 `json:"targetId,omitzero"`
	InstantiationDepth int    `json:"instantiationDepth,omitzero"`
	InstantiationCount int    `json:"instantiationCount,omitzero"`
	EstimatedCount     int    `json:"estimatedCount,omitzero"`
	Size               int    `json:"size,omitzero"`
	Depth              int    `json:"depth,omitzero"`
	TargetDepth        int    `json:"targetDepth,omitzero"`
	NumCombinations    int    `json:"numCombinations,omitzero"`
	SourceSize         int    `json:"sourceSize,omitzero"`
	TargetSize         int    `json:"targetSize,omitzero"`
	Parent             uint32 `json:"parent,omitzero"`
	ID                 uint32 `json:"id,omitzero"`
	Arity              int    `json:"arity,omitzero"`
	CheckerID          int    `json:"checkerId,omitzero"`
}

// parseTraceEvents reads trace.json back from the trace sink and normalizes it
// into Spans and Instants. Type descriptors are parsed separately so the traced
// compiler Program can be released before large types_N.json payloads are
// decoded.
func (c *Capture) parseTraceEvents(fs vfs.FS) error {
	// Pair begin/end events per thread (LIFO by name) into durations.
	type openEvent struct {
		name string
		ts   float64
		ev   traceEnvelopeEvent
	}
	stacks := map[int][]openEvent{}

	dec, done, err := jsonDecoderFor(fs, traceDir+"/trace.json")
	if err != nil {
		return err
	}
	defer done()
	if err := readJSONArray(dec, func(ev traceEnvelopeEvent) error {
		switch ev.PH {
		case "B":
			stacks[ev.TID] = append(stacks[ev.TID], openEvent{name: ev.Name, ts: ev.TS, ev: ev})
		case "E":
			stack := stacks[ev.TID]
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == ev.Name {
					begin := stack[i]
					stacks[ev.TID] = append(stack[:i], stack[i+1:]...)
					c.Spans = append(c.Spans, c.spanFrom(begin.ev, ev.TS-begin.ts, false))
					break
				}
			}
		case "X":
			if ev.Dur != nil {
				sp := c.spanFrom(ev, *ev.Dur, true)
				if sp.Path == "" {
					c.recordTypeOriginIDs(ev.Args)
				}
				c.Spans = append(c.Spans, sp)
			}
		case "I":
			c.Instants = append(c.Instants, Instant{Phase: ev.Cat, Name: ev.Name, Args: ev.Args})
			c.recordTypeOriginIDs(ev.Args)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("perf: parse trace: %w", err)
	}
	fs.Remove(traceDir + "/trace.json")
	return nil
}

// parseTraceTypes reads the per-checker types_*.json files back from the
// trace sink and aggregates the type data needed by the report.
func (c *Capture) parseTraceTypes(fs vfs.FS) error {
	// Read recorded type descriptors from every checker's types_N.json. The
	// trace legend lists the checker-specific type files.
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
		dec, done, err := jsonDecoderFor(fs, p)
		if err != nil {
			continue
		}
		err = readJSONArray(dec, func(desc tracing.TypeDescriptor) error {
			c.recordTypeDescriptor(&desc)
			return nil
		})
		done()
		if err != nil {
			continue
		}
		fs.Remove(p)
	}
	return nil
}

func (c *Capture) recordTypeDescriptor(desc *tracing.TypeDescriptor) {
	var origin *typeOrigin
	if desc.FirstDeclaration != nil {
		canon := c.canonical(desc.FirstDeclaration.Path)
		o := typeOrigin{canon: canon}
		if desc.FirstDeclaration.Start.Line > 0 {
			o.line = desc.FirstDeclaration.Start.Line
		}
		if _, needed := c.typeOriginIDs[desc.ID]; needed {
			c.typeOrigins[desc.ID] = o
		}
		c.typeCounts[canon]++
		origin = &o
	}

	name := desc.SymbolName
	if name == "" {
		name = desc.IntrinsicName
	}
	if name == "" {
		return
	}
	if c.hotTypesAll != nil {
		c.recordHotType(c.hotTypesAll, name, desc, origin)
	}
	if origin != nil && c.inProject(origin.canon) {
		c.recordHotType(c.hotTypesProject, name, desc, origin)
	}
}

func (c *Capture) recordHotType(m map[string]*hotTypeAgg, name string, desc *tracing.TypeDescriptor, origin *typeOrigin) {
	a := m[name]
	if a == nil {
		a = &hotTypeAgg{ht: &HotType{Symbol: name}}
		m[name] = a
	}
	a.ht.Count++
	if len(desc.UnionTypes) > a.ht.MaxUnion {
		a.ht.MaxUnion = len(desc.UnionTypes)
	}
	if hasString(desc.Flags, "Conditional") {
		a.ht.Conditional++
	}
	if !a.haveOrigin && origin != nil {
		a.ht.File = c.display(origin.canon)
		a.ht.Line = origin.line
		a.haveOrigin = true
	}
}

func hasString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (c *Capture) recordTypeOriginIDs(args traceArgs) {
	for _, id := range []uint32{args.TypeID, args.SourceID, args.TargetID} {
		if id != 0 {
			c.typeOriginIDs[id] = struct{}{}
		}
	}
}

func jsonDecoderFor(fs vfs.FS, path string) (*json.Decoder, func(), error) {
	if d, ok := fs.(*diskTraceFS); ok {
		real := d.real(path)
		f, err := os.Open(real)
		if err != nil {
			return nil, func() {}, fmt.Errorf("perf: trace output missing: %w", err)
		}
		return json.NewDecoder(f), func() { _ = f.Close() }, nil
	}
	raw, ok := fs.ReadFile(path)
	if !ok {
		return nil, func() {}, fmt.Errorf("perf: trace output missing")
	}
	return json.NewDecoder(strings.NewReader(raw)), func() {}, nil
}

func readJSONArray[T any](dec *json.Decoder, each func(T) error) error {
	token, err := dec.ReadToken()
	if err != nil {
		if err == io.EOF {
			return fmt.Errorf("empty JSON input")
		}
		return err
	}
	if token.Kind() != json.BeginArray.Kind() {
		return fmt.Errorf("expected JSON array, got %q", token.Kind())
	}
	for dec.PeekKind() != json.EndArray.Kind() {
		var item T
		if err := json.UnmarshalDecode(dec, &item); err != nil {
			return err
		}
		if err := each(item); err != nil {
			return err
		}
	}
	_, err = dec.ReadToken()
	return err
}

func (c *Capture) spanFrom(ev traceEnvelopeEvent, durUS float64, sampled bool) Span {
	s := Span{Phase: ev.Cat, Name: ev.Name, DurUS: durUS, Sampled: sampled}
	s.Path = ev.Args.Path
	s.Pos = ev.Args.Pos
	if sampled && s.Path == "" {
		args := ev.Args
		s.Args = &args
	}
	return s
}
