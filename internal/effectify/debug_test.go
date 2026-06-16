package effectify

// Temporary debugging harness: EFFECTIFY_DEBUG_FILE=/path/to/file.ts go test
// -run TestEffectifyDebug ./internal/effectify -v
// Prints the raw rewriter output (no verification) plus all parse diagnostics
// with surrounding context.

import (
	"fmt"
	"os"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
)

func TestEffectifyDebugParseETS(t *testing.T) {
	path := os.Getenv("EFFECTIFY_DEBUG_ETS")
	if path == "" {
		t.Skip("set EFFECTIFY_DEBUG_ETS")
	}
	srcBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file := parseFile("/case.ets", string(srcBytes), core.ScriptKindETS)
	fmt.Println("diagnostics:", len(file.Diagnostics()))
	for _, d := range file.Diagnostics() {
		fmt.Printf("  TS%d at %d\n", d.Code(), d.Pos())
	}
	fmt.Println(printNormalized(file))
}

func TestEffectifyDebug(t *testing.T) {
	path := os.Getenv("EFFECTIFY_DEBUG_FILE")
	if path == "" {
		t.Skip("set EFFECTIFY_DEBUG_FILE")
	}
	srcBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(srcBytes)

	file := parseFile("/case.ts", src, core.ScriptKindTS)
	if len(file.Diagnostics()) > 0 {
		t.Fatalf("input has TS parse errors: %v", file.Diagnostics()[0])
	}
	bindings := scanBindings(file, "effect")
	if reason := bindings.skipReason(); reason != "" {
		t.Fatalf("skip: %s", reason)
	}
	r := &rewriter{src: src, file: file, localToCanonical: bindings.localToCanonical, barrelRoots: bindings.barrelRoots, bound: bindings.bound}
	out := r.emit(file.AsNode())

	etsFile := parseFile("/case.ets", out, core.ScriptKindETS)
	for _, d := range etsFile.Diagnostics() {
		pos := d.Pos()
		lo, hi := pos-200, pos+200
		if lo < 0 {
			lo = 0
		}
		if hi > len(out) {
			hi = len(out)
		}
		fmt.Printf("=== TS%d at %d ===\n%s\n<<<HERE>>>\n%s\n", d.Code(), pos, out[lo:pos], out[pos:hi])
	}
	if len(etsFile.Diagnostics()) == 0 {
		before := parseFile("/before.ts", flattenPipes("/before.ts", printNormalized(file)), core.ScriptKindTS)
		after := parseFile("/after.ts", flattenPipes("/after.ts", printNormalized(etsFile)), core.ScriptKindTS)
		if !equalModuloParens(before.AsNode(), after.AsNode()) {
			fmt.Println("=== round-trip mismatch ===")
			if os.Getenv("EFFECTIFY_DEBUG_DUMP") != "" {
				fmt.Printf("--- before (desugared original) ---\n%s\n--- after (desugared output) ---\n%s\n", printNormalized(before), printNormalized(after))
			}
		} else {
			fmt.Println("=== converts cleanly ===")
		}
	}
	fmt.Printf("stats: %s\n", r.stats.String())
}
