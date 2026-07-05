package perf

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/tracing"
)

func TestRecordTypeDescriptorStoresOnlyNeededOrigins(t *testing.T) {
	c := &Capture{
		typeCounts:      map[string]int{},
		typeOriginIDs:   map[uint32]struct{}{1: {}},
		typeOrigins:     map[uint32]typeOrigin{},
		hotTypesAll:     map[string]*hotTypeAgg{},
		hotTypesProject: map[string]*hotTypeAgg{},
	}

	c.recordTypeDescriptor(&tracing.TypeDescriptor{
		ID:         1,
		SymbolName: "Needed",
		FirstDeclaration: &tracing.Location{
			Path:  "/project/src/needed.ts",
			Start: &tracing.LineAndChar{Line: 7},
		},
	})
	c.recordTypeDescriptor(&tracing.TypeDescriptor{
		ID:         2,
		SymbolName: "Unneeded",
		FirstDeclaration: &tracing.Location{
			Path:  "/project/src/unneeded.ts",
			Start: &tracing.LineAndChar{Line: 11},
		},
	})

	if got := c.fileOfTypeArgs(map[string]any{"typeId": float64(1)}); got != "/project/src/needed.ts" {
		t.Fatalf("needed type origin = %q, want /project/src/needed.ts", got)
	}
	if got := c.fileOfTypeArgs(map[string]any{"typeId": float64(2)}); got != "" {
		t.Fatalf("unneeded type origin = %q, want empty", got)
	}
	if c.typeCounts["/project/src/needed.ts"] != 1 || c.typeCounts["/project/src/unneeded.ts"] != 1 {
		t.Fatalf("type counts = %#v, want counts for both declarations", c.typeCounts)
	}
	if len(c.HotTypes(false)) != 2 {
		t.Fatalf("hot type aggregation dropped descriptors: %#v", c.HotTypes(false))
	}
}

func TestSpanFromRetainsOnlyNeededArgs(t *testing.T) {
	c := &Capture{}
	withPath := traceEnvelopeEvent{
		Cat:  "check",
		Name: "checkSourceFile",
		Args: map[string]any{"path": "/project/src/a.ts", "pos": float64(10), "typeId": float64(1)},
	}
	withoutPath := traceEnvelopeEvent{
		Cat:  "check",
		Name: "structuredTypeRelatedTo",
		Args: map[string]any{"sourceId": float64(1), "targetId": float64(2)},
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
