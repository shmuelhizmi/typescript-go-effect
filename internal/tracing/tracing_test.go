package tracing

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/json"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

func TestConcurrentDurationEventsUseSeparateThreadIDs(t *testing.T) {
	t.Parallel()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	tr, err := StartTracing(fsys, "/trace", "", true /*deterministic*/)
	assert.NilError(t, err)

	endA := tr.Push(PhaseParse, "createSourceFile", map[string]any{"path": "/a.ts"}, true)
	endB := tr.Push(PhaseParse, "createSourceFile", map[string]any{"path": "/b.ts"}, true)
	endA()
	endB()

	endCheck := tr.Push(PhaseCheck, "checkSourceFile", map[string]any{"checkerId": 0, "path": "/a.ts"}, true)
	endVariance := tr.Push(PhaseCheckTypes, "getVariancesWorker", map[string]any{"checkerId": 0, "id": 1}, true)
	endVariance()
	endCheck()

	assert.NilError(t, tr.StopTracing())

	traceText, ok := fsys.ReadFile("/trace/trace.json")
	assert.Assert(t, ok)

	var events []traceEvent
	assert.NilError(t, json.Unmarshal([]byte(traceText), &events))

	aBegin := findEvent(t, events, "B", "createSourceFile", "path", "/a.ts")
	aEnd := findEvent(t, events, "E", "createSourceFile", "path", "/a.ts")
	bBegin := findEvent(t, events, "B", "createSourceFile", "path", "/b.ts")
	bEnd := findEvent(t, events, "E", "createSourceFile", "path", "/b.ts")
	assert.Equal(t, aBegin.TID, aEnd.TID)
	assert.Equal(t, bBegin.TID, bEnd.TID)
	assert.Assert(t, aBegin.TID != bBegin.TID)
	assertThreadName(t, events, aBegin.TID, "file:/a.ts")
	assertThreadName(t, events, bBegin.TID, "file:/b.ts")

	checkBegin := findEvent(t, events, "B", "checkSourceFile", "path", "/a.ts")
	varianceBegin := findEvent(t, events, "B", "getVariancesWorker", "id", float64(1))
	assert.Equal(t, checkBegin.TID, varianceBegin.TID)
	assertThreadName(t, events, checkBegin.TID, "checker:0")

	assertDurationEventsAreWellNestedByThread(t, events)
}

func TestThreadIDsAreStableAcrossFirstSeenOrder(t *testing.T) {
	t.Parallel()

	first := traceThreadIDsForPaths(t, []string{"/a.ts", "/b.ts"})
	second := traceThreadIDsForPaths(t, []string{"/b.ts", "/a.ts"})

	assert.DeepEqual(t, first, second)
}

func TestTypeDisplayCanBeDisabled(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		options Options
		want    bool
	}{
		{name: "default", options: Options{IncludeTypeDisplay: true}, want: true},
		{name: "disabled", options: Options{IncludeTypeDisplay: false}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fsys := vfstest.FromMap(fstest.MapFS{
				"/trace": &fstest.MapFile{Mode: fs.ModeDir},
			}, true)

			tr, err := StartTracingWithOptions(fsys, "/trace", "", true /*deterministic*/, tt.options)
			assert.NilError(t, err)
			tracer := tr.NewTypeTracer(0)
			tracer.RecordType(testTracedType{id: 1, display: "display text"})
			assert.NilError(t, tr.StopTracing())

			typesText, ok := fsys.ReadFile("/trace/types_0.json")
			assert.Assert(t, ok)
			assert.Equal(t, strings.Contains(typesText, `"display"`), tt.want)
		})
	}
}

func TestStopTracingReleasesTypeTracerTypes(t *testing.T) {
	t.Parallel()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	tr, err := StartTracingWithOptions(fsys, "/trace", "", true /*deterministic*/, Options{IncludeTypeDisplay: false})
	assert.NilError(t, err)
	tracer := tr.NewTypeTracer(0)
	typedTracer := tracer.(*typeTracer)
	tracer.RecordType(testTracedType{id: 1})
	assert.Equal(t, len(typedTracer.types), 1)

	assert.NilError(t, tr.StopTracing())
	assert.Equal(t, len(typedTracer.types), 0)
	assert.Equal(t, len(tr.tracers), 0)
}

func TestStreamedTypeDescriptorsDoNotRetainTypes(t *testing.T) {
	t.Parallel()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	tr, err := StartTracingWithOptions(fsys, "/trace", "", true /*deterministic*/, Options{
		IncludeTypeDisplay:    false,
		StreamTypeDescriptors: true,
	})
	assert.NilError(t, err)
	tracer := tr.NewTypeTracer(0)
	typedTracer := tracer.(*typeTracer)
	tracer.RecordType(testTracedType{id: 1})
	assert.Equal(t, len(typedTracer.types), 1)

	assert.NilError(t, tr.FlushTypeDescriptors())
	assert.Equal(t, len(typedTracer.types), 0)
	assert.Equal(t, typedTracer.typeCount, 1)

	assert.NilError(t, tr.StopTracing())
	typesText, ok := fsys.ReadFile("/trace/types_0.json")
	assert.Assert(t, ok)
	assert.Assert(t, strings.Contains(typesText, `"id":1`))
	assert.Equal(t, len(typedTracer.types), 0)
}

func TestStreamedTypeDescriptorSinkCanSkipTypeFiles(t *testing.T) {
	t.Parallel()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	var seen []TypeDescriptor
	tr, err := StartTracingWithOptions(fsys, "/trace", "", true /*deterministic*/, Options{
		IncludeTypeDisplay:      false,
		StreamTypeDescriptors:   true,
		TypeDescriptorSink:      func(desc TypeDescriptor) error { seen = append(seen, desc); return nil },
		SkipTypeDescriptorFiles: true,
	})
	assert.NilError(t, err)
	tracer := tr.NewTypeTracer(0)
	typedTracer := tracer.(*typeTracer)
	tracer.RecordType(testTracedType{id: 1})

	assert.NilError(t, tr.FlushTypeDescriptors())
	assert.Equal(t, len(typedTracer.types), 0)
	assert.Equal(t, len(seen), 1)
	assert.Equal(t, seen[0].ID, uint32(1))

	assert.NilError(t, tr.StopTracing())
	_, ok := fsys.ReadFile("/trace/types_0.json")
	assert.Assert(t, !ok, "types file should be skipped")
}

func TestStreamedTypeDescriptorsRejectDisplayStrings(t *testing.T) {
	t.Parallel()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	_, err := StartTracingWithOptions(fsys, "/trace", "", true /*deterministic*/, Options{
		IncludeTypeDisplay:    true,
		StreamTypeDescriptors: true,
	})
	assert.ErrorContains(t, err, "cannot include type display")
}

type testTracedType struct {
	id      uint32
	display string
}

func (t testTracedType) Id() uint32                              { return t.id }
func (t testTracedType) FormatFlags() []string                   { return []string{"Object"} }
func (t testTracedType) IsConditional() bool                     { return false }
func (t testTracedType) Symbol() *ast.Symbol                     { return nil }
func (t testTracedType) AliasSymbol() *ast.Symbol                { return nil }
func (t testTracedType) AliasTypeArguments() []TracedType        { return nil }
func (t testTracedType) IntrinsicName() string                   { return "" }
func (t testTracedType) UnionTypes() []TracedType                { return nil }
func (t testTracedType) IntersectionTypes() []TracedType         { return nil }
func (t testTracedType) IndexType() TracedType                   { return nil }
func (t testTracedType) IndexedAccessObjectType() TracedType     { return nil }
func (t testTracedType) IndexedAccessIndexType() TracedType      { return nil }
func (t testTracedType) ConditionalCheckType() TracedType        { return nil }
func (t testTracedType) ConditionalExtendsType() TracedType      { return nil }
func (t testTracedType) ConditionalTrueType() TracedType         { return nil }
func (t testTracedType) ConditionalFalseType() TracedType        { return nil }
func (t testTracedType) SubstitutionBaseType() TracedType        { return nil }
func (t testTracedType) SubstitutionConstraintType() TracedType  { return nil }
func (t testTracedType) ReferenceTarget() TracedType             { return nil }
func (t testTracedType) ReferenceTypeArguments() []TracedType    { return nil }
func (t testTracedType) ReferenceNode() *ast.Node                { return nil }
func (t testTracedType) ReverseMappedSourceType() TracedType     { return nil }
func (t testTracedType) ReverseMappedMappedType() TracedType     { return nil }
func (t testTracedType) ReverseMappedConstraintType() TracedType { return nil }
func (t testTracedType) EvolvingArrayElementType() TracedType    { return nil }
func (t testTracedType) EvolvingArrayFinalType() TracedType      { return nil }
func (t testTracedType) IsTuple() bool                           { return false }
func (t testTracedType) Pattern() *ast.Node                      { return nil }
func (t testTracedType) RecursionIdentity() any                  { return nil }
func (t testTracedType) Display() string                         { return t.display }

func traceThreadIDsForPaths(t *testing.T, paths []string) map[string]int {
	t.Helper()

	fsys := vfstest.FromMap(fstest.MapFS{
		"/trace": &fstest.MapFile{Mode: fs.ModeDir},
	}, true)

	tr, err := StartTracing(fsys, "/trace", "", true /*deterministic*/)
	assert.NilError(t, err)

	for _, path := range paths {
		end := tr.Push(PhaseParse, "createSourceFile", map[string]any{"path": path}, true)
		end()
	}

	assert.NilError(t, tr.StopTracing())

	traceText, ok := fsys.ReadFile("/trace/trace.json")
	assert.Assert(t, ok)

	var events []traceEvent
	assert.NilError(t, json.Unmarshal([]byte(traceText), &events))

	threadIDs := make(map[string]int)
	for _, path := range paths {
		threadIDs[path] = findEvent(t, events, "B", "createSourceFile", "path", path).TID
	}
	return threadIDs
}

func findEvent(t *testing.T, events []traceEvent, phase string, name string, argName string, argValue any) traceEvent {
	t.Helper()
	for _, event := range events {
		if event.PH == phase && event.Name == name && event.Args[argName] == argValue {
			return event
		}
	}
	t.Fatalf("failed to find %s event %q with %s=%v", phase, name, argName, argValue)
	return traceEvent{}
}

func assertThreadName(t *testing.T, events []traceEvent, tid int, name string) {
	t.Helper()
	for _, event := range events {
		if event.PH == "M" && event.Name == "thread_name" && event.TID == tid && event.Args["name"] == name {
			return
		}
	}
	t.Fatalf("failed to find thread_name metadata for thread %d named %q", tid, name)
}

func assertDurationEventsAreWellNestedByThread(t *testing.T, events []traceEvent) {
	t.Helper()

	stacks := make(map[int][]traceEvent)
	for _, event := range events {
		switch event.PH {
		case "B":
			stacks[event.TID] = append(stacks[event.TID], event)
		case "E":
			stack := stacks[event.TID]
			assert.Assert(t, len(stack) > 0, "unmatched end event %q on thread %d", event.Name, event.TID)
			begin := stack[len(stack)-1]
			assert.Equal(t, begin.Cat, event.Cat)
			assert.Equal(t, begin.Name, event.Name)
			stacks[event.TID] = stack[:len(stack)-1]
		}
	}

	for tid, stack := range stacks {
		assert.Assert(t, len(stack) == 0, "thread %d has %d unterminated events", tid, len(stack))
	}
}
