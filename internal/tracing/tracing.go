package tracing

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/json"
	"github.com/microsoft/typescript-go/internal/scanner"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/zeebo/xxh3"
)

// Tracer is an interface for recording types during type checking.
// Each checker should have its own Tracer instance to avoid sharing types between checkers.
type Tracer interface {
	// RecordType records a type for later dumping.
	RecordType(t TracedType)
	// DumpTypes writes all recorded types to disk.
	DumpTypes() error
}

// TracedType is an interface that represents a type that can be traced.
// This allows the tracing package to work with types from the checker package
// without creating a circular dependency.
type TracedType interface {
	Id() uint32
	FormatFlags() []string
	IsConditional() bool
	Symbol() *ast.Symbol
	AliasSymbol() *ast.Symbol
	AliasTypeArguments() []TracedType

	// Type-specific data accessors
	IntrinsicName() string
	UnionTypes() []TracedType
	IntersectionTypes() []TracedType
	IndexType() TracedType
	IndexedAccessObjectType() TracedType
	IndexedAccessIndexType() TracedType
	ConditionalCheckType() TracedType
	ConditionalExtendsType() TracedType
	ConditionalTrueType() TracedType
	ConditionalFalseType() TracedType
	SubstitutionBaseType() TracedType
	SubstitutionConstraintType() TracedType
	ReferenceTarget() TracedType
	ReferenceTypeArguments() []TracedType
	ReferenceNode() *ast.Node
	ReverseMappedSourceType() TracedType
	ReverseMappedMappedType() TracedType
	ReverseMappedConstraintType() TracedType
	EvolvingArrayElementType() TracedType
	EvolvingArrayFinalType() TracedType
	IsTuple() bool
	Pattern() *ast.Node
	RecursionIdentity() any

	// Display is an optional string representation of the type
	Display() string
}

// TraceRecord represents metadata about a single trace file
type TraceRecord struct {
	ConfigFilePath string `json:"configFilePath,omitzero"`
	TracePath      string `json:"tracePath,omitzero"`
	TypesPath      string `json:"typesPath,omitzero"`
	CheckerID      int    `json:"checkerId"`
}

// Options controls optional trace output details.
type Options struct {
	// IncludeTypeDisplay records display strings in types_N.json. This preserves
	// the normal --generateTrace shape, but callers that only need type IDs,
	// flags, and declarations can disable it to avoid expensive TypeToString work.
	IncludeTypeDisplay bool
	// StreamTypeDescriptors writes type descriptors as types are recorded instead
	// of retaining TracedType references until StopTracing. It is only supported
	// when IncludeTypeDisplay is false, because Display() can re-enter the checker.
	StreamTypeDescriptors bool
}

type traceEvent struct {
	PID  int            `json:"pid"`
	TID  int            `json:"tid"`
	PH   string         `json:"ph"`
	Cat  string         `json:"cat"`
	TS   float64        `json:"ts"`
	Name string         `json:"name,omitzero"`
	S    string         `json:"s,omitzero"` // scope, only set for instant events ("g" = global)
	Dur  *float64       `json:"dur,omitzero"`
	Args map[string]any `json:"args,omitzero"`
}

// sampleInterval matches TypeScript's 10ms sampling interval.
// Events with separateBeginAndEnd=false are only recorded if their
// duration crosses a 10ms sampling boundary.
const sampleInterval = 10 * time.Millisecond

const traceFileName = "trace.json"

const (
	mainThreadID           = 1
	firstSyntheticThreadID = 2
	firstFileThreadID      = 1_000_000
	fileThreadIDHashRange  = 1_000_000_000
)

var traceThreadArgKeys = [...]string{"path", "fileName", "containingFileName", "jsFilePath", "declarationFilePath"}

// flushThreshold is the size at which buffered trace content is flushed to disk
// via AppendFile. Keeps peak memory bounded for long-running compilations while
// avoiding a syscall per event.
const flushThreshold = 256 * 1024

// typeFlushThreshold is larger because type descriptor files are written in a
// tight single-threaded dump loop; a larger buffer keeps syscall overhead down
// while still avoiding the previous full-file string builder.
const typeFlushThreshold = 8 * 1024 * 1024

// Tracing manages the overall tracing session including all checkers
type Tracing struct {
	fs                    vfs.FS
	traceDir              string
	tracePath             string
	configFilePath        string
	legend                []TraceRecord
	tracers               []*typeTracer
	traceContent          strings.Builder
	traceStarted          atomic.Bool
	threadIDs             map[traceThreadKey]int
	threadKeys            map[int]traceThreadKey
	metadataTS            float64
	deterministic         bool // when true, use monotonic counter instead of real time
	includeTypeDisplay    bool
	streamTypeDescriptors bool
	timestampCounter      uint64 // only used in deterministic mode
	startTime             time.Time
	mu                    sync.Mutex
	// flushErr holds the first error encountered while appending the trace buffer
	// to disk. Once set, subsequent flushes become no-ops and the error is
	// surfaced from StopTracing so that transient I/O failures (disk full,
	// permission denied, etc.) don't crash the compiler.
	flushErr error
}

// Phase represents a tracing phase
type Phase string

const (
	PhaseParse      Phase = "parse"
	PhaseProgram    Phase = "program"
	PhaseBind       Phase = "bind"
	PhaseCheck      Phase = "check"
	PhaseCheckTypes Phase = "checkTypes"
	PhaseEmit       Phase = "emit"
	PhaseSession    Phase = "session"
)

// StartTracing creates a new tracing session.
// When deterministic is true, timestamps use a monotonic counter instead of
// real wall-clock time, producing stable output for test baselines.
func StartTracing(fs vfs.FS, traceDir string, configFilePath string, deterministic bool) (*Tracing, error) {
	return StartTracingWithOptions(fs, traceDir, configFilePath, deterministic, Options{IncludeTypeDisplay: true})
}

// StartTracingWithOptions creates a new tracing session with optional output
// controls. Most callers should use StartTracing to preserve default trace
// contents.
func StartTracingWithOptions(fs vfs.FS, traceDir string, configFilePath string, deterministic bool, opts Options) (*Tracing, error) {
	if opts.StreamTypeDescriptors && opts.IncludeTypeDisplay {
		return nil, fmt.Errorf("streaming type descriptors cannot include type display strings")
	}
	tr := &Tracing{
		fs:                    fs,
		traceDir:              traceDir,
		tracePath:             tspath.CombinePaths(traceDir, traceFileName),
		configFilePath:        configFilePath,
		legend:                []TraceRecord{},
		tracers:               []*typeTracer{},
		threadIDs:             make(map[traceThreadKey]int),
		threadKeys:            make(map[int]traceThreadKey),
		deterministic:         deterministic,
		includeTypeDisplay:    opts.IncludeTypeDisplay,
		streamTypeDescriptors: opts.StreamTypeDescriptors,
		startTime:             time.Now(),
	}
	tr.traceStarted.Store(true)

	// Write the trace file header with metadata events
	tr.traceContent.WriteString("[\n")

	// Write metadata events (matching TypeScript's format)
	metaTs := tr.timestamp()
	tr.metadataTS = metaTs
	tr.writeEvent(traceEvent{PID: 1, TID: mainThreadID, PH: "M", Cat: "__metadata", TS: metaTs, Name: "process_name", Args: map[string]any{"name": "tsgo"}})
	tr.traceContent.WriteString(",\n")
	tr.writeEvent(traceEvent{PID: 1, TID: mainThreadID, PH: "M", Cat: "__metadata", TS: metaTs, Name: "thread_name", Args: map[string]any{"name": "Main"}})
	tr.traceContent.WriteString(",\n")
	tr.writeEvent(traceEvent{PID: 1, TID: mainThreadID, PH: "M", Cat: "disabled-by-default-devtools.timeline", TS: metaTs, Name: "TracingStartedInBrowser"})

	// Truncate any existing trace file with the header so subsequent AppendFile
	// calls extend a clean file.
	if err := tr.fs.WriteFile(tr.tracePath, tr.traceContent.String()); err != nil {
		return nil, fmt.Errorf("failed to write trace file header: %w", err)
	}
	tr.traceContent.Reset()

	return tr, nil
}

// timestamp returns the current timestamp in microseconds.
// In deterministic mode it returns a monotonically increasing counter;
// otherwise it returns the real elapsed wall-clock time since tracing started,
// matching TypeScript's 1000 * timestamp() (microseconds).
func (tr *Tracing) timestamp() float64 {
	if tr.deterministic {
		tr.timestampCounter++
		return float64(tr.timestampCounter)
	}
	return float64(time.Since(tr.startTime).Nanoseconds()) / 1000.0
}

func writeEventTo(buf *strings.Builder, event traceEvent) {
	if err := json.MarshalWrite(buf, event, json.Deterministic(true)); err != nil {
		panic(fmt.Sprintf("failed to marshal trace event: %v", err))
	}
}

func (tr *Tracing) writeEvent(event traceEvent) {
	writeEventTo(&tr.traceContent, event)
}

// maybeFlushLocked appends the buffered trace content to disk if it has grown
// past the flush threshold. Caller must hold tr.mu. If a previous flush failed,
// or this flush fails, the error is recorded in tr.flushErr and subsequent
// writes become no-ops; the error is surfaced from StopTracing.
func (tr *Tracing) maybeFlushLocked() {
	if tr.flushErr != nil {
		tr.traceContent.Reset()
		return
	}
	if tr.traceContent.Len() < flushThreshold {
		return
	}
	if err := tr.fs.AppendFile(tr.tracePath, tr.traceContent.String()); err != nil {
		tr.flushErr = fmt.Errorf("failed to flush trace file: %w", err)
	}
	tr.traceContent.Reset()
}

// Instant records an instant event in the trace.
// Safe to call on nil receiver.
func (tr *Tracing) Instant(phase Phase, name string, args map[string]any) {
	if tr == nil || !tr.traceStarted.Load() {
		return
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()

	// Re-check under the lock: StopTracing may have run between the load above
	// and acquiring the lock. Once stopped, further writes would land in a buffer
	// that has already been flushed and the closing "]" written.
	if !tr.traceStarted.Load() {
		return
	}

	ts := tr.timestamp()
	tid := tr.threadIDLocked(args)
	tr.traceContent.WriteString(",\n")
	tr.writeEvent(traceEvent{PID: 1, TID: tid, PH: "I", Cat: string(phase), TS: ts, Name: name, S: "g", Args: args})
	tr.maybeFlushLocked()
}

// Push starts a trace event block on the shared trace buffer.
// Safe to call on nil receiver. Safe to call from multiple goroutines.
//
// When separateBeginAndEnd is true, a "B" (begin) event is written immediately and
// the returned function writes a matching "E" (end) event. This is used for events
// that must always appear in the trace (e.g. checkSourceFile, createProgram, emit).
//
// When separateBeginAndEnd is false (the default in TypeScript), the event is only
// recorded if its duration crosses a 10ms sampling boundary, matching TypeScript's
// behavior of sampling short-lived events to avoid trace bloat.
//
// Returns a function that should be called (typically deferred) to end the event.
// Each returned function is self-contained and does not depend on a shared stack,
// making it safe for concurrent use across goroutines.
func (tr *Tracing) Push(phase Phase, name string, args map[string]any, separateBeginAndEnd bool) func() {
	if tr == nil || !tr.traceStarted.Load() {
		return func() {}
	}

	if separateBeginAndEnd {
		tr.mu.Lock()
		if !tr.traceStarted.Load() {
			tr.mu.Unlock()
			return func() {}
		}
		ts := tr.timestamp()
		tid := tr.threadIDLocked(args)
		tr.traceContent.WriteString(",\n")
		tr.writeEvent(traceEvent{PID: 1, TID: tid, PH: "B", Cat: string(phase), TS: ts, Name: name, Args: args})
		tr.maybeFlushLocked()
		tr.mu.Unlock()

		return func() {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			if !tr.traceStarted.Load() {
				return
			}
			endTs := tr.timestamp()
			tr.traceContent.WriteString(",\n")
			tr.writeEvent(traceEvent{PID: 1, TID: tid, PH: "E", Cat: string(phase), TS: endTs, Name: name, Args: args})
			tr.maybeFlushLocked()
		}
	}

	// Sampled event: only record if duration crosses a sampling boundary.
	// In deterministic mode, sampled events are skipped entirely to avoid flaky baselines,
	// so avoid the cost of cloning args / capturing the start time.
	if tr.deterministic {
		return func() {}
	}
	startTime := time.Now()
	args = maps.Clone(args)
	return func() {
		dur := float64(time.Since(startTime).Nanoseconds()) / 1000.0
		startMicros := float64(startTime.Sub(tr.startTime).Nanoseconds()) / 1000.0
		intervalMicros := float64(sampleInterval.Nanoseconds()) / 1000.0
		if intervalMicros-math.Mod(startMicros, intervalMicros) > dur {
			return
		}
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if !tr.traceStarted.Load() {
			return
		}
		tid := tr.threadIDLocked(args)
		tr.traceContent.WriteString(",\n")
		tr.writeEvent(traceEvent{PID: 1, TID: tid, PH: "X", Cat: string(phase), TS: startMicros, Name: name, Dur: &dur, Args: args})
		tr.maybeFlushLocked()
	}
}

func (tr *Tracing) threadIDLocked(args map[string]any) int {
	key, ok := traceThreadKeyFromArgs(args)
	if !ok {
		return mainThreadID
	}

	if tid, ok := tr.threadIDs[key]; ok {
		return tid
	}

	tid := key.defaultThreadID()
	for {
		if existingKey, ok := tr.threadKeys[tid]; !ok || existingKey == key {
			break
		}
		tid++
	}
	tr.threadIDs[key] = tid
	tr.threadKeys[tid] = key
	tr.writeThreadNameEventLocked(tid, key.displayName())
	return tid
}

func (tr *Tracing) writeThreadNameEventLocked(tid int, name string) {
	tr.traceContent.WriteString(",\n")
	tr.writeEvent(traceEvent{PID: 1, TID: tid, PH: "M", Cat: "__metadata", TS: tr.metadataTS, Name: "thread_name", Args: map[string]any{"name": name}})
}

type traceThreadKind string

const (
	traceThreadKindChecker traceThreadKind = "checker"
	traceThreadKindFile    traceThreadKind = "file"
)

type traceThreadKey struct {
	kind     traceThreadKind
	text     string
	index    int
	hasIndex bool
}

func traceThreadKeyFromArgs(args map[string]any) (traceThreadKey, bool) {
	if len(args) == 0 {
		return traceThreadKey{}, false
	}

	if checkerID, ok := args["checkerId"].(int); ok {
		return traceThreadKey{kind: traceThreadKindChecker, index: checkerID, hasIndex: true}, true
	}

	for _, key := range traceThreadArgKeys {
		if value, ok := args[key]; ok {
			if path, ok := value.(string); ok && path != "" {
				return traceThreadKey{kind: traceThreadKindFile, text: path}, true
			}
		}
	}

	return traceThreadKey{}, false
}

func (key traceThreadKey) defaultThreadID() int {
	if key.kind == traceThreadKindChecker && key.hasIndex && key.index >= 0 {
		return firstSyntheticThreadID + key.index
	}
	return stableTraceThreadID(key)
}

func (key traceThreadKey) displayName() string {
	if key.hasIndex {
		return string(key.kind) + ":" + strconv.Itoa(key.index)
	}
	return string(key.kind) + ":" + key.text
}

func stableTraceThreadID(key traceThreadKey) int {
	hash := xxh3.New()
	_, _ = hash.WriteString(string(key.kind))
	_, _ = hash.WriteString(":")
	if key.hasIndex {
		_, _ = hash.WriteString(strconv.Itoa(key.index))
	} else {
		_, _ = hash.WriteString(key.text)
	}
	return firstFileThreadID + int(hash.Sum64()%fileThreadIDHashRange)
}

// NewTypeTracer creates a new tracer for a specific checker.
// The checkerIndex is used to create unique filenames for each checker's output.
func (tr *Tracing) NewTypeTracer(checkerIndex int) Tracer {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	typesPath := tspath.CombinePaths(tr.traceDir, fmt.Sprintf("types_%d.json", checkerIndex))
	tracer := &typeTracer{
		fs:                 tr.fs,
		checkerIndex:       checkerIndex,
		typesPath:          typesPath,
		includeTypeDisplay: tr.includeTypeDisplay,
		streamDescriptors:  tr.streamTypeDescriptors,
		types:              []TracedType{},
	}
	tr.tracers = append(tr.tracers, tracer)
	tr.legend = append(tr.legend, TraceRecord{
		ConfigFilePath: tr.configFilePath,
		TracePath:      tr.tracePath,
		TypesPath:      typesPath,
		CheckerID:      checkerIndex,
	})
	return tracer
}

// FlushTypeDescriptors streams and releases any type descriptors recorded so
// far for tracing sessions created with Options.StreamTypeDescriptors. It is a
// no-op for normal tracing sessions, which preserve TypeScript's deferred
// types_N.json dump shape.
func (tr *Tracing) FlushTypeDescriptors() error {
	for _, tracer := range tr.tracers {
		if err := tracer.FlushTypes(); err != nil {
			return fmt.Errorf("failed to flush types for checker %d: %w", tracer.checkerIndex, err)
		}
	}
	return nil
}

// StopTracing finalizes the tracing session and writes all output files
func (tr *Tracing) StopTracing() error {
	// Dump types from all tracers BEFORE acquiring the lock, because
	// DumpTypes → buildTypeDescriptor → Display() → TypeToString can
	// re-enter the checker which calls Push/Pop (which need tr.mu).
	for _, tracer := range tr.tracers {
		if err := tracer.DumpTypes(); err != nil {
			return fmt.Errorf("failed to dump types for checker %d: %w", tracer.checkerIndex, err)
		}
	}
	tr.tracers = nil

	tr.mu.Lock()
	defer tr.mu.Unlock()

	// Close the trace file(s)
	if tr.traceStarted.Load() {
		// Surface any buffered flush failure before attempting the final write.
		if tr.flushErr != nil {
			tr.traceContent.Reset()
			tr.traceStarted.Store(false)
			return tr.flushErr
		}
		// Flush any remaining buffered content and close the JSON array.
		if err := tr.fs.AppendFile(tr.tracePath, tr.traceContent.String()+"\n]\n"); err != nil {
			return fmt.Errorf("failed to write trace file: %w", err)
		}
		tr.traceContent.Reset()
		tr.traceStarted.Store(false)
	}

	// Sort legend entries by typesPath for deterministic output
	slices.SortFunc(tr.legend, func(a, b TraceRecord) int {
		return strings.Compare(a.TypesPath, b.TypesPath)
	})

	// Write the legend file
	legendPath := tspath.CombinePaths(tr.traceDir, "legend.json")
	legendData, err := json.MarshalIndent(tr.legend, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal legend file: %w", err)
	}
	if err := tr.fs.WriteFile(legendPath, string(legendData)); err != nil {
		return fmt.Errorf("failed to write legend file: %w", err)
	}

	return nil
}

// typeTracer is the per-checker tracer implementation
type typeTracer struct {
	fs                 vfs.FS
	checkerIndex       int
	typesPath          string
	includeTypeDisplay bool
	streamDescriptors  bool
	types              []TracedType
	typeContent        strings.Builder
	typeCount          int
	typeFlushErr       error
	mu                 sync.Mutex
}

func (t *typeTracer) RecordType(typ TracedType) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.types = append(t.types, typ)
}

func (t *typeTracer) writeTypeDescriptor(descriptor TypeDescriptor, id uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.typeFlushErr != nil {
		return
	}
	if t.typeCount == 0 {
		if err := t.fs.WriteFile(t.typesPath, "["); err != nil {
			t.typeFlushErr = fmt.Errorf("failed to write types file header: %w", err)
			return
		}
	} else {
		t.typeContent.WriteString(",\n")
	}

	if err := json.MarshalWrite(&t.typeContent, descriptor); err != nil {
		t.typeFlushErr = fmt.Errorf("failed to marshal type %d: %w", id, err)
		return
	}
	t.typeCount++
	if t.typeContent.Len() >= typeFlushThreshold {
		if err := t.flushTypeContentLocked(); err != nil {
			t.typeFlushErr = fmt.Errorf("failed to flush types file: %w", err)
		}
	}
}

func (t *typeTracer) FlushTypes() error {
	if !t.streamDescriptors {
		return nil
	}

	t.mu.Lock()
	types := slices.Clone(t.types)
	t.types = nil
	t.mu.Unlock()

	for i, typ := range types {
		descriptor := t.buildTypeDescriptor(typ, nil)
		t.writeTypeDescriptor(descriptor, typ.Id())
		types[i] = nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	return t.typeFlushErr
}

func (t *typeTracer) DumpTypes() error {
	if t.streamDescriptors {
		if err := t.FlushTypes(); err != nil {
			return err
		}
		return t.finishStreamedTypes()
	}

	// Copy the types slice under lock, then release so Display() calls during
	// buildTypeDescriptor don't deadlock when they create new types.
	t.mu.Lock()
	types := slices.Clone(t.types)
	t.types = nil
	t.mu.Unlock()
	defer t.clearTypes()

	if len(types) == 0 {
		return nil
	}

	// Write the opening bracket separately so the descriptor buffer can be
	// flushed in bounded chunks instead of retaining the whole types_N.json.
	if err := t.fs.WriteFile(t.typesPath, "["); err != nil {
		return fmt.Errorf("failed to write types file header: %w", err)
	}

	var sb strings.Builder
	recursionIdentityMap := make(map[any]int)
	recursionToken := func(identity any) int {
		token, ok := recursionIdentityMap[identity]
		if !ok {
			token = len(recursionIdentityMap)
			recursionIdentityMap[identity] = token
		}
		return token
	}
	flush := func() error {
		if sb.Len() == 0 {
			return nil
		}
		if err := t.fs.AppendFile(t.typesPath, sb.String()); err != nil {
			return err
		}
		sb.Reset()
		return nil
	}

	for i, typ := range types {
		descriptor := t.buildTypeDescriptor(typ, recursionToken)

		if err := json.MarshalWrite(&sb, descriptor); err != nil {
			return fmt.Errorf("failed to marshal type %d: %w", typ.Id(), err)
		}

		if i < len(types)-1 {
			sb.WriteString(",\n")
		}
		if sb.Len() >= typeFlushThreshold {
			if err := flush(); err != nil {
				return fmt.Errorf("failed to flush types file: %w", err)
			}
		}
	}

	sb.WriteString("]\n")

	if err := flush(); err != nil {
		return fmt.Errorf("failed to write types file: %w", err)
	}
	return nil
}

func (t *typeTracer) finishStreamedTypes() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.typeFlushErr != nil {
		t.typeContent.Reset()
		return t.typeFlushErr
	}
	if t.typeCount == 0 {
		return nil
	}

	t.typeContent.WriteString("]\n")
	if err := t.flushTypeContentLocked(); err != nil {
		t.typeContent.Reset()
		return fmt.Errorf("failed to write types file: %w", err)
	}
	return nil
}

func (t *typeTracer) flushTypeContentLocked() error {
	if t.typeContent.Len() == 0 {
		return nil
	}
	if err := t.fs.AppendFile(t.typesPath, t.typeContent.String()); err != nil {
		return err
	}
	t.typeContent.Reset()
	return nil
}

func (t *typeTracer) clearTypes() {
	t.mu.Lock()
	t.types = nil
	t.mu.Unlock()
}

// TypeDescriptor represents a type in the output JSON
type TypeDescriptor struct {
	ID                      uint32   `json:"id"`
	IntrinsicName           string   `json:"intrinsicName,omitzero"`
	SymbolName              string   `json:"symbolName,omitzero"`
	RecursionID             *int     `json:"recursionId,omitzero"`
	IsTuple                 bool     `json:"isTuple,omitzero"`
	UnionTypes              []uint32 `json:"unionTypes,omitzero"`
	IntersectionTypes       []uint32 `json:"intersectionTypes,omitzero"`
	AliasTypeArguments      []uint32 `json:"aliasTypeArguments,omitzero"`
	KeyofType               *uint32  `json:"keyofType,omitzero"`
	IndexedAccessObjectType *uint32  `json:"indexedAccessObjectType,omitzero"`
	IndexedAccessIndexType  *uint32  `json:"indexedAccessIndexType,omitzero"`
	ConditionalCheckType    *uint32  `json:"conditionalCheckType,omitzero"`
	ConditionalExtendsType  *uint32  `json:"conditionalExtendsType,omitzero"`
	// ConditionalTrueType and ConditionalFalseType are *int32 (not *uint32) because
	// unresolved conditional branches are serialized as -1, matching TypeScript's behavior.
	ConditionalTrueType         *int32    `json:"conditionalTrueType,omitzero"`
	ConditionalFalseType        *int32    `json:"conditionalFalseType,omitzero"`
	SubstitutionBaseType        *uint32   `json:"substitutionBaseType,omitzero"`
	ConstraintType              *uint32   `json:"constraintType,omitzero"`
	InstantiatedType            *uint32   `json:"instantiatedType,omitzero"`
	TypeArguments               []uint32  `json:"typeArguments,omitzero"`
	ReferenceLocation           *Location `json:"referenceLocation,omitzero"`
	ReverseMappedSourceType     *uint32   `json:"reverseMappedSourceType,omitzero"`
	ReverseMappedMappedType     *uint32   `json:"reverseMappedMappedType,omitzero"`
	ReverseMappedConstraintType *uint32   `json:"reverseMappedConstraintType,omitzero"`
	EvolvingArrayElementType    *uint32   `json:"evolvingArrayElementType,omitzero"`
	EvolvingArrayFinalType      *uint32   `json:"evolvingArrayFinalType,omitzero"`
	DestructuringPattern        *Location `json:"destructuringPattern,omitzero"`
	FirstDeclaration            *Location `json:"firstDeclaration,omitzero"`
	Flags                       []string  `json:"flags"`
	Display                     string    `json:"display,omitzero"`
}

// Location represents a source code location
type Location struct {
	Path  string       `json:"path"`
	Start *LineAndChar `json:"start,omitzero"`
	End   *LineAndChar `json:"end,omitzero"`
}

// LineAndChar represents a line and character position (1-indexed)
type LineAndChar struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (t *typeTracer) buildTypeDescriptor(typ TracedType, recursionToken func(any) int) TypeDescriptor {
	symbol := typ.Symbol()
	aliasSymbol := typ.AliasSymbol()

	desc := TypeDescriptor{
		ID:    typ.Id(),
		Flags: typ.FormatFlags(),
	}

	// Assign a unique integer token per recursion identity, matching TypeScript's behavior.
	// This lets trace analysis tools detect which types share the same recursion identity.
	if recursionToken != nil {
		if identity := typ.RecursionIdentity(); identity != nil {
			token := recursionToken(identity)
			desc.RecursionID = &token
		}
	}

	// Intrinsic name
	if name := typ.IntrinsicName(); name != "" {
		desc.IntrinsicName = name
	}

	// Symbol name - escape the internal symbol name prefix for valid JSON
	if sym := aliasSymbol; sym != nil {
		desc.SymbolName = ast.EscapeAllInternalSymbolNames(sym.Name)
	} else if symbol != nil {
		desc.SymbolName = ast.EscapeAllInternalSymbolNames(symbol.Name)
	}

	// Tuple flag
	if typ.IsTuple() {
		desc.IsTuple = true
	}

	// Union types
	if types := typ.UnionTypes(); len(types) > 0 {
		desc.UnionTypes = mapTypeIds(types)
	}

	// Intersection types
	if types := typ.IntersectionTypes(); len(types) > 0 {
		desc.IntersectionTypes = mapTypeIds(types)
	}

	// Alias type arguments
	if args := typ.AliasTypeArguments(); len(args) > 0 {
		desc.AliasTypeArguments = mapTypeIds(args)
	}

	// Index type (keyof)
	if indexType := typ.IndexType(); indexType != nil {
		desc.KeyofType = new(indexType.Id())
	}

	// Indexed access type
	if objType := typ.IndexedAccessObjectType(); objType != nil {
		desc.IndexedAccessObjectType = new(objType.Id())
	}
	if idxType := typ.IndexedAccessIndexType(); idxType != nil {
		desc.IndexedAccessIndexType = new(idxType.Id())
	}

	// Conditional type
	if typ.IsConditional() {
		if checkType := typ.ConditionalCheckType(); checkType != nil {
			desc.ConditionalCheckType = new(checkType.Id())
		}
		if extendsType := typ.ConditionalExtendsType(); extendsType != nil {
			desc.ConditionalExtendsType = new(extendsType.Id())
		}
		if trueType := typ.ConditionalTrueType(); trueType != nil {
			desc.ConditionalTrueType = new(int32(trueType.Id()))
		} else {
			desc.ConditionalTrueType = new(int32(-1))
		}
		if falseType := typ.ConditionalFalseType(); falseType != nil {
			desc.ConditionalFalseType = new(int32(falseType.Id()))
		} else {
			desc.ConditionalFalseType = new(int32(-1))
		}
	}

	// Substitution type
	if baseType := typ.SubstitutionBaseType(); baseType != nil {
		desc.SubstitutionBaseType = new(baseType.Id())
	}
	if constraint := typ.SubstitutionConstraintType(); constraint != nil {
		desc.ConstraintType = new(constraint.Id())
	}

	// Reference type
	if target := typ.ReferenceTarget(); target != nil {
		desc.InstantiatedType = new(target.Id())
	}
	if args := typ.ReferenceTypeArguments(); len(args) > 0 {
		desc.TypeArguments = mapTypeIds(args)
	}
	if node := typ.ReferenceNode(); node != nil {
		desc.ReferenceLocation = getLocation(node)
	}

	// Reverse mapped type
	if sourceType := typ.ReverseMappedSourceType(); sourceType != nil {
		desc.ReverseMappedSourceType = new(sourceType.Id())
	}
	if mappedType := typ.ReverseMappedMappedType(); mappedType != nil {
		desc.ReverseMappedMappedType = new(mappedType.Id())
	}
	if constraintType := typ.ReverseMappedConstraintType(); constraintType != nil {
		desc.ReverseMappedConstraintType = new(constraintType.Id())
	}

	// Evolving array type
	if elemType := typ.EvolvingArrayElementType(); elemType != nil {
		desc.EvolvingArrayElementType = new(elemType.Id())
	}
	if finalType := typ.EvolvingArrayFinalType(); finalType != nil {
		desc.EvolvingArrayFinalType = new(finalType.Id())
	}

	// Pattern (destructuring)
	if pattern := typ.Pattern(); pattern != nil {
		desc.DestructuringPattern = getLocation(pattern)
	}

	// First declaration - prefer aliasSymbol, matching TypeScript's `aliasSymbol ?? symbol`
	firstDeclSymbol := aliasSymbol
	if firstDeclSymbol == nil {
		firstDeclSymbol = symbol
	}
	if firstDeclSymbol != nil && len(firstDeclSymbol.Declarations) > 0 {
		desc.FirstDeclaration = getLocation(firstDeclSymbol.Declarations[0])
	}

	if t.includeTypeDisplay {
		// Display text
		if display := typ.Display(); display != "" {
			desc.Display = display
		}
	}

	return desc
}

func mapTypeIds(types []TracedType) []uint32 {
	if len(types) == 0 {
		return nil
	}
	ids := make([]uint32, len(types))
	for i, t := range types {
		if t != nil {
			ids[i] = t.Id()
		}
	}
	return ids
}

func getLocation(node *ast.Node) *Location {
	if node == nil {
		return nil
	}
	file := ast.GetSourceFileOfNode(node)
	if file == nil {
		return nil
	}

	startPos := scanner.GetTokenPosOfNode(node, file, false)
	startLine, startChar := scanner.GetECMALineAndUTF16CharacterOfPosition(file, startPos)
	endLine, endChar := scanner.GetECMALineAndUTF16CharacterOfPosition(file, node.End())

	return &Location{
		Path: string(tspath.ToPath(file.FileName(), "", false)),
		Start: &LineAndChar{
			Line:      startLine + 1,
			Character: int(startChar) + 1,
		},
		End: &LineAndChar{
			Line:      endLine + 1,
			Character: int(endChar) + 1,
		},
	}
}
