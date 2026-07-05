package perf

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/json"
	"github.com/microsoft/typescript-go/internal/tracing"
)

func TestRecordTypeDescriptorStoresOnlyNeededOrigins(t *testing.T) {
	c := &Capture{
		typeCounts:      map[string]int{},
		typeOriginIDs:   map[uint32]struct{}{1: {}},
		typeOrigins:     map[uint32]uint32{},
		hotTypesAll:     map[string]*hotTypeAgg{},
		hotTypesProject: map[string]*hotTypeAgg{},
	}

	c.recordTypeDescriptor(&tracing.TypeDescriptor{
		ID:         1,
		SymbolName: "Needed",
		FirstDeclaration: &tracing.Location{
			Path:  "/project/src/needed.ts",
			Start: tracing.LineAndChar{Line: 7},
		},
	})
	c.recordTypeDescriptor(&tracing.TypeDescriptor{
		ID:         2,
		SymbolName: "Unneeded",
		FirstDeclaration: &tracing.Location{
			Path:  "/project/src/unneeded.ts",
			Start: tracing.LineAndChar{Line: 11},
		},
	})

	if got := c.fileOfTypeArgs(traceArgs{TypeID: 1}); got != "/project/src/needed.ts" {
		t.Fatalf("needed type origin = %q, want /project/src/needed.ts", got)
	}
	if got := c.fileOfTypeArgs(traceArgs{TypeID: 2}); got != "" {
		t.Fatalf("unneeded type origin = %q, want empty", got)
	}
	if c.typeCounts["/project/src/needed.ts"] != 1 || c.typeCounts["/project/src/unneeded.ts"] != 1 {
		t.Fatalf("type counts = %#v, want counts for both declarations", c.typeCounts)
	}
	if len(c.HotTypes(false)) != 2 {
		t.Fatalf("hot type aggregation dropped descriptors: %#v", c.HotTypes(false))
	}
}

func TestRecordTypeDescriptorCanSkipAllHotTypes(t *testing.T) {
	c := &Capture{
		typeCounts:      map[string]int{},
		typeOriginIDs:   map[uint32]struct{}{},
		typeOrigins:     map[uint32]uint32{},
		hotTypesProject: map[string]*hotTypeAgg{},
	}

	c.recordTypeDescriptor(&tracing.TypeDescriptor{
		ID:         1,
		SymbolName: "ProjectType",
		FirstDeclaration: &tracing.Location{
			Path: "/project/src/project.ts",
		},
	})

	if c.hotTypesAll != nil {
		t.Fatal("all hot-type aggregation should remain disabled")
	}
	if got := c.HotTypes(false); len(got) != 1 || got[0].Symbol != "ProjectType" {
		t.Fatalf("project hot types = %#v, want ProjectType", got)
	}
	if got := c.HotTypes(true); len(got) != 1 || got[0].Symbol != "ProjectType" {
		t.Fatalf("include-libs on a project-only capture = %#v, want project fallback", got)
	}
}

func TestSpanFromRetainsOnlyNeededArgs(t *testing.T) {
	c := &Capture{}
	withPath := traceEnvelopeEvent{
		Cat:  "check",
		Name: "checkSourceFile",
		Args: traceArgs{Path: "/project/src/a.ts", Pos: 10, TypeID: 1},
	}
	withoutPath := traceEnvelopeEvent{
		Cat:  "check",
		Name: "structuredTypeRelatedTo",
		Args: traceArgs{SourceID: 1, TargetID: 2},
	}

	if sp := c.spanFrom(withPath, 100, false); sp.Args != nil {
		t.Fatalf("unsampled span retained args: %#v", sp.Args)
	}
	if sp := c.spanFrom(withPath, 100, true); sp.Args != nil {
		t.Fatalf("sampled path span retained args: %#v", sp.Args)
	}
	if sp := c.spanFrom(withoutPath, 100, true); sp.Args == nil {
		t.Fatal("sampled type-id span should retain args for origin attribution")
	}
}

func TestTraceEnvelopeDecodesTypedArgs(t *testing.T) {
	raw := []byte(`{"ph":"I","cat":"checkTypes","name":"checkTypeRelatedTo_DepthLimit","args":{"sourceId":17,"targetId":23,"depth":4,"targetDepth":5,"checkerId":1}}`)
	var ev traceEnvelopeEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Args.SourceID != 17 || ev.Args.TargetID != 23 || ev.Args.Depth != 4 || ev.Args.TargetDepth != 5 || ev.Args.CheckerID != 1 {
		t.Fatalf("decoded args = %#v", ev.Args)
	}
	if got := formatArgs(ev.Args); got != "sourceId=17 targetId=23 depth=4 targetDepth=5 checkerId=1" {
		t.Fatalf("formatArgs = %q", got)
	}
}
