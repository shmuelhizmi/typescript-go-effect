package cmds

import (
	"context"
	"strings"
	"testing"
)

const typeTargetsSource = `export interface Shape {
	kind: "circle" | "square";
	radius?: number;
}

export type Status = "on" | "off" | number;

export interface Box<T> {
	value: T;
}

export const box: Box<string> = { value: "hi" };
`

func TestTypeAt(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/types.ts": typeTargetsSource})
	ctx := context.Background()

	// Union via symbol ID.
	result, err := runTypeAt(ctx, ws, &typeAtFlags{expandDepth: 1, symbol: "src/types.ts#Status"}, nil)
	if err != nil {
		t.Fatalf("runTypeAt(Status): %v", err)
	}
	if result.Total() != 1 {
		t.Fatalf("expected 1 entry, got %d", result.Total())
	}
	status := result.Entries[0]
	if status.Type == nil || status.Type.Kind != "union" {
		t.Fatalf("Status type = %+v, want union", status.Type)
	}
	if len(status.Type.UnionMembers) != 3 {
		t.Errorf("Status union members = %d, want 3", len(status.Type.UnionMembers))
	}
	for _, want := range []string{`"on"`, `"off"`, "number"} {
		if !strings.Contains(status.Display, want) {
			t.Errorf("Status display %q missing %s", status.Display, want)
		}
	}
	if status.SymbolID != "src/types.ts#Status" {
		t.Errorf("Status symbolId = %q", status.SymbolID)
	}
	if !strings.HasPrefix(status.Declaration, "src/types.ts:6:") {
		t.Errorf("Status declaration = %q, want src/types.ts:6:…", status.Declaration)
	}

	// Object via symbol ID.
	result, err = runTypeAt(ctx, ws, &typeAtFlags{expandDepth: 1, symbol: "src/types.ts#Shape"}, nil)
	if err != nil {
		t.Fatalf("runTypeAt(Shape): %v", err)
	}
	shape := result.Entries[0]
	if shape.Type.Kind != "object" {
		t.Errorf("Shape kind = %q, want object", shape.Type.Kind)
	}
	if len(shape.Type.Properties) != 2 {
		t.Fatalf("Shape properties = %d, want 2", len(shape.Type.Properties))
	}
	var foundOptionalRadius bool
	for _, p := range shape.Type.Properties {
		if p.Name == "radius" && p.Optional && strings.Contains(p.Type, "number") {
			foundOptionalRadius = true
		}
	}
	if !foundOptionalRadius {
		t.Errorf("Shape properties = %+v, want optional radius: number", shape.Type.Properties)
	}

	// Generic instance via position: `box` on line 12, column 14.
	result, err = runTypeAt(ctx, ws, &typeAtFlags{expandDepth: 2}, []string{"src/types.ts:12:14"})
	if err != nil {
		t.Fatalf("runTypeAt(box position): %v", err)
	}
	box := result.Entries[0]
	if box.Display != "Box<string>" {
		t.Errorf("box display = %q, want Box<string>", box.Display)
	}
	if box.Type.Kind != "reference" {
		t.Errorf("box kind = %q, want reference", box.Type.Kind)
	}
	if len(box.Type.TypeArguments) != 1 || box.Type.TypeArguments[0].Display != "string" {
		t.Errorf("box typeArguments = %+v, want [string]", box.Type.TypeArguments)
	}
	if len(box.Type.Properties) != 1 || box.Type.Properties[0].Name != "value" || box.Type.Properties[0].Type != "string" {
		t.Errorf("box properties = %+v, want value: string", box.Type.Properties)
	}
	if box.SymbolID != "src/types.ts#box" {
		t.Errorf("box symbolId = %q", box.SymbolID)
	}
}

func TestTypeAtExpandDepthZero(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/types.ts": typeTargetsSource})
	result, err := runTypeAt(context.Background(), ws, &typeAtFlags{expandDepth: 0, symbol: "src/types.ts#Status"}, nil)
	if err != nil {
		t.Fatalf("runTypeAt: %v", err)
	}
	entry := result.Entries[0]
	if len(entry.Type.UnionMembers) != 0 {
		t.Errorf("expand-depth 0 should not expand union members, got %d", len(entry.Type.UnionMembers))
	}
	if entry.Type.Display == "" {
		t.Error("expand-depth 0 should still carry the display string")
	}
}

const assignSource = `export interface Point {
	x: number;
	y: number;
}

export interface Point3 {
	x: number;
	y: number;
	z: number;
}

export interface Mismatch {
	x: string;
	y: number;
}
`

func TestTypeAssignableTrue(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/assign.ts": assignSource})
	result, err := runTypeAssignable(context.Background(), ws, &assignableFlags{
		source: "src/assign.ts#Point3",
		to:     "src/assign.ts#Point",
	}, nil)
	if err != nil {
		t.Fatalf("runTypeAssignable: %v", err)
	}
	if !result.Assignable {
		t.Errorf("Point3 should be assignable to Point, got %+v", result)
	}
	if len(result.Problems) != 0 {
		t.Errorf("no problems expected on success, got %+v", result.Problems)
	}
	if result.Source != "Point3" || result.Target != "Point" {
		t.Errorf("displays = %q -> %q", result.Source, result.Target)
	}
}

func TestTypeAssignableFalseDrilldown(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/assign.ts": assignSource})
	ctx := context.Background()

	// Missing property.
	result, err := runTypeAssignable(ctx, ws, &assignableFlags{
		source: "src/assign.ts#Point",
		to:     "src/assign.ts#Point3",
	}, nil)
	if err != nil {
		t.Fatalf("runTypeAssignable: %v", err)
	}
	if result.Assignable {
		t.Fatal("Point should not be assignable to Point3")
	}
	if len(result.Problems) != 1 || result.Problems[0].Property != "z" ||
		result.Problems[0].Expected != "number" || result.Problems[0].Actual != "missing" {
		t.Errorf("problems = %+v, want z: expected number, actual missing", result.Problems)
	}
	if result.Note == "" {
		t.Error("failure result should carry the approximation note")
	}

	// Mismatched property type.
	result, err = runTypeAssignable(ctx, ws, &assignableFlags{
		source: "src/assign.ts#Mismatch",
		to:     "src/assign.ts#Point",
	}, nil)
	if err != nil {
		t.Fatalf("runTypeAssignable: %v", err)
	}
	if result.Assignable {
		t.Fatal("Mismatch should not be assignable to Point")
	}
	if len(result.Problems) != 1 || result.Problems[0].Property != "x" ||
		result.Problems[0].Expected != "number" || result.Problems[0].Actual != "string" {
		t.Errorf("problems = %+v, want x: expected number, actual string", result.Problems)
	}
}

const coverageSource = `export function risky(a: any, s: string): string {
	const sum = a + s;
	const propAny = a.foo;
	const u = s as unknown;
	const nn = s!;
	// @ts-ignore
	const bad: number = s;
	void u;
	void bad;
	void propAny;
	return sum + nn;
}
`

func TestTypeCoverage(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/coverage.ts": coverageSource,
		"/project/src/clean.ts":    "export const n: number = 1;\n",
	})
	result, err := runTypeCoverage(context.Background(), ws, &coverageFlags{threshold: -1}, nil)
	if err != nil {
		t.Fatalf("runTypeCoverage: %v", err)
	}
	var cov *FileCoverage
	for _, f := range result.Files {
		if f.File == "src/coverage.ts" {
			cov = f
		}
	}
	if cov == nil {
		t.Fatalf("no coverage for src/coverage.ts: %+v", result.Files)
	}
	if cov.Expressions == 0 {
		t.Fatal("expected counted expressions")
	}
	if cov.Any < 2 {
		t.Errorf("any count = %d, want >= 2 (a and any-typed sum usages)", cov.Any)
	}
	if cov.Unknown < 1 {
		t.Errorf("unknown count = %d, want >= 1", cov.Unknown)
	}
	if cov.Casts != 1 {
		t.Errorf("casts = %d, want 1", cov.Casts)
	}
	if cov.NonNull != 1 {
		t.Errorf("nonNull = %d, want 1", cov.NonNull)
	}
	if cov.Ignores != 1 {
		t.Errorf("ignores = %d, want 1", cov.Ignores)
	}
	if cov.AnyPct <= 0 {
		t.Errorf("anyPct = %v, want > 0", cov.AnyPct)
	}
	if result.Total.Any != cov.Any || result.Total.Expressions < cov.Expressions {
		t.Errorf("totals not aggregated: %+v", result.Total)
	}
}

func TestTypeCoverageThreshold(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/coverage.ts": coverageSource})
	ctx := context.Background()

	result, err := runTypeCoverage(ctx, ws, &coverageFlags{threshold: 0}, nil)
	if err != nil {
		t.Fatalf("runTypeCoverage: %v", err)
	}
	if !result.ThresholdExceeded {
		t.Errorf("threshold 0 should be exceeded (anyPct=%v)", result.Total.AnyPct)
	}

	result, err = runTypeCoverage(ctx, ws, &coverageFlags{threshold: 100}, nil)
	if err != nil {
		t.Fatalf("runTypeCoverage: %v", err)
	}
	if result.ThresholdExceeded {
		t.Errorf("threshold 100 should not be exceeded (anyPct=%v)", result.Total.AnyPct)
	}
}

const complexitySource = `export type Simple = string;

export type Deep<T> = T extends string
	? { a: T; b: number }
	: never;

type Hidden = { a: string };

export const value = 1;
`

func TestTypeComplexityRanking(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/complexity.ts": complexitySource})
	result, err := runTypeComplexity(context.Background(), ws, &complexityFlags{rank: true, top: 50}, nil)
	if err != nil {
		t.Fatalf("runTypeComplexity: %v", err)
	}
	if result.Total() != 2 {
		t.Fatalf("expected 2 exported type declarations (Simple, Deep), got %d: %+v", result.Total(), result.Entries)
	}
	first, second := result.Entries[0], result.Entries[1]
	if first.Name != "Deep" || second.Name != "Simple" {
		t.Errorf("ranking = [%s %s], want [Deep Simple]", first.Name, second.Name)
	}
	if first.Complexity <= second.Complexity {
		t.Errorf("Deep complexity %d should exceed Simple %d", first.Complexity, second.Complexity)
	}
	if second.Complexity != 1 {
		t.Errorf("Simple complexity = %d, want 1", second.Complexity)
	}
	if first.SymbolID != "src/complexity.ts#Deep" {
		t.Errorf("Deep symbolId = %q", first.SymbolID)
	}
	if first.Line != 3 {
		t.Errorf("Deep line = %d, want 3", first.Line)
	}

	// Threshold drops Simple; top caps the list.
	result, err = runTypeComplexity(context.Background(), ws, &complexityFlags{rank: true, top: 50, threshold: 1}, nil)
	if err != nil {
		t.Fatalf("runTypeComplexity(threshold): %v", err)
	}
	if result.Total() != 1 || result.Entries[0].Name != "Deep" {
		t.Errorf("threshold 1 should keep only Deep, got %+v", result.Entries)
	}
}

func TestTypeComplexitySymbolMode(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/complexity.ts": complexitySource})
	result, err := runTypeComplexity(context.Background(), ws, &complexityFlags{rank: true, top: 50, symbol: "src/complexity.ts#Deep"}, nil)
	if err != nil {
		t.Fatalf("runTypeComplexity(--symbol): %v", err)
	}
	if result.Total() != 1 || result.Entries[0].Name != "Deep" || result.Entries[0].Complexity < 2 {
		t.Errorf("symbol mode result = %+v", result.Entries)
	}
}

const instSource = `export interface Box<T> {
	value: T;
}

export function id<T>(x: T): T {
	return x;
}

export const a: Box<string> = { value: "a" };
export const b: Box<string> = { value: "b" };
export const c: Box<number> = { value: 1 };

export const n = id(42);
export const s = id("hello");
`

func TestTypeInstantiations(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/inst.ts": instSource})
	ctx := context.Background()

	// Generic type used three times, two distinct instantiations.
	result, err := runTypeInstantiations(ctx, ws, &instantiationsFlags{}, []string{"src/inst.ts#Box"})
	if err != nil {
		t.Fatalf("runTypeInstantiations(Box): %v", err)
	}
	if len(result.Instantiations) != 2 {
		t.Fatalf("Box instantiations = %+v, want 2 distinct", result.Instantiations)
	}
	first := result.Instantiations[0]
	if first.Display != "Box<string>" || first.Count != 2 {
		t.Errorf("top instantiation = %+v, want Box<string> x2", first)
	}
	if len(first.TypeArguments) != 1 || first.TypeArguments[0] != "string" {
		t.Errorf("Box<string> typeArguments = %v", first.TypeArguments)
	}
	if len(first.Locations) != 2 {
		t.Errorf("Box<string> locations = %v, want 2", first.Locations)
	}
	second := result.Instantiations[1]
	if second.Display != "Box<number>" || second.Count != 1 {
		t.Errorf("second instantiation = %+v, want Box<number> x1", second)
	}
	if result.Method != "refs" || result.Note == "" {
		t.Errorf("result should declare the refs method and limitation note, got method=%q", result.Method)
	}

	// Generic function called twice with different inferred arguments.
	result, err = runTypeInstantiations(ctx, ws, &instantiationsFlags{}, []string{"src/inst.ts#id"})
	if err != nil {
		t.Fatalf("runTypeInstantiations(id): %v", err)
	}
	if len(result.Instantiations) != 2 {
		t.Fatalf("id instantiations = %+v, want 2 distinct calls", result.Instantiations)
	}
	joined := result.Instantiations[0].Display + " | " + result.Instantiations[1].Display
	if !strings.Contains(joined, "42") || !strings.Contains(joined, `"hello"`) {
		t.Errorf("id instantiations = %q, want literal-inferred signatures for 42 and \"hello\"", joined)
	}
	for _, inst := range result.Instantiations {
		if inst.Kind != "call" || inst.Count != 1 {
			t.Errorf("call instantiation = %+v, want kind=call count=1", inst)
		}
	}
}

// ---------------------------------------------------------------------------
// type explain-error

const explainSource = `export interface Inner {
	value: number;
}

export interface Outer {
	name: string;
	inner: Inner;
}

export const candidate = {
	name: "x",
	inner: { value: "bad" },
};

export const o: Outer = candidate;
`

func TestTypeExplainErrorNestedAssignability(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/err.ts": explainSource})
	ctx := context.Background()

	// Position form: the error is reported on `o` (line 15, col 14).
	result, err := runTypeExplainError(ctx, ws, &explainErrorFlags{}, []string{"src/err.ts:15:14"})
	if err != nil {
		t.Fatalf("runTypeExplainError(position): %v", err)
	}
	if result.Diagnostic == nil || result.Diagnostic.Code != "TS2322" {
		t.Fatalf("diagnostic = %+v, want TS2322", result.Diagnostic)
	}
	if result.Diagnostic.DiagRef == "" {
		t.Error("diagnostic should carry a diagRef")
	}

	// Message chain decomposition: nested causes at depth >= 1.
	if len(result.Chain) < 2 {
		t.Fatalf("chain = %+v, want root plus at least one nested cause", result.Chain)
	}
	if result.Chain[0].Depth != 0 || result.Chain[0].Code != "TS2322" {
		t.Errorf("chain root = %+v, want depth 0 code TS2322", result.Chain[0])
	}
	maxDepth := 0
	for _, entry := range result.Chain {
		maxDepth = max(maxDepth, entry.Depth)
	}
	if maxDepth < 1 {
		t.Errorf("chain max depth = %d, want >= 1 (nested causes)", maxDepth)
	}

	// Drill-down names the mismatched property path inner -> value.
	if result.Drilldown == nil {
		t.Fatal("expected an assignability drilldown for TS2322")
	}
	if result.Drilldown.Target != "Outer" {
		t.Errorf("drilldown target = %q, want Outer", result.Drilldown.Target)
	}
	var inner *AssignabilityProblem
	for _, p := range result.Drilldown.Problems {
		if p.Property == "inner" {
			inner = p
		}
	}
	if inner == nil {
		t.Fatalf("drilldown problems = %+v, want property inner", result.Drilldown.Problems)
	}
	if len(inner.Nested) != 1 || inner.Nested[0].Property != "value" ||
		inner.Nested[0].Expected != "number" || inner.Nested[0].Actual != "string" {
		t.Errorf("nested problems = %+v, want value: expected number, got string", inner.Nested)
	}
}

func TestTypeExplainErrorDiagRefRoundTrip(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/err.ts": explainSource})
	ctx := context.Background()

	// Run check and feed its diagRef back into explain-error.
	checkResult, err := runCheck(ctx, ws, &checkFlags{code: "TS2322"}, nil)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if len(checkResult.Diagnostics) == 0 {
		t.Fatal("expected a TS2322 diagnostic from check")
	}
	diagRef := checkResult.Diagnostics[0].DiagRef

	result, err := runTypeExplainError(ctx, ws, &explainErrorFlags{}, []string{diagRef})
	if err != nil {
		t.Fatalf("runTypeExplainError(%s): %v", diagRef, err)
	}
	if result.Diagnostic.DiagRef != diagRef {
		t.Errorf("round-trip diagRef = %q, want %q", result.Diagnostic.DiagRef, diagRef)
	}
	if result.Diagnostic.Message != checkResult.Diagnostics[0].Message {
		t.Errorf("message mismatch: %q vs %q", result.Diagnostic.Message, checkResult.Diagnostics[0].Message)
	}
	if result.Drilldown == nil {
		t.Error("expected drilldown via diagRef form too")
	}
}

func TestTypeExplainErrorNotFound(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/err.ts": explainSource})
	if _, err := runTypeExplainError(context.Background(), ws, &explainErrorFlags{}, []string{"src/err.ts:1:1"}); err == nil {
		t.Error("expected not-found error for a clean position")
	}
	if _, err := runTypeExplainError(context.Background(), ws, &explainErrorFlags{}, []string{"src/err.ts:9999:TS2322"}); err == nil {
		t.Error("expected not-found error for a stale diagRef")
	}
}

// ---------------------------------------------------------------------------
// type flow

const flowSource = `export function f(x: string | undefined): string {
	if (x) {
		return x;
	}
	return "";
}
`

func TestTypeFlowNarrowing(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/flow.ts": flowSource})

	// `x` parameter declaration at line 1, col 19.
	result, err := runTypeFlow(context.Background(), ws, &flowFlags{}, []string{"src/flow.ts:1:19"})
	if err != nil {
		t.Fatalf("runTypeFlow: %v", err)
	}
	if result.DeclaredType != "string | undefined" {
		t.Errorf("declared type = %q, want string | undefined", result.DeclaredType)
	}
	if len(result.Rows) < 3 {
		t.Fatalf("rows = %+v, want declaration + 2 references", result.Rows)
	}
	if !result.Rows[0].Declaration || result.Rows[0].Type != "string | undefined" {
		t.Errorf("first row = %+v, want declaration row with declared type", result.Rows[0])
	}

	// Inside `if (x) { return x; }` the reference must be narrowed to string.
	var narrowed *FlowRow
	for _, row := range result.Rows {
		if row.Context == "return x;" {
			narrowed = row
		}
	}
	if narrowed == nil {
		t.Fatalf("no row for `return x;`: %+v", result.Rows)
	}
	if narrowed.Type != "string" {
		t.Errorf("narrowed type = %q, want string (flow narrowing via GetTypeAtLocation)", narrowed.Type)
	}
	if narrowed.NarrowedFrom != "string | undefined" {
		t.Errorf("narrowedFrom = %q, want string | undefined", narrowed.NarrowedFrom)
	}
	if result.Note != "" {
		t.Errorf("note should be empty when narrowing is observed, got %q", result.Note)
	}

	// The condition reference `if (x)` is not narrowed yet.
	var condition *FlowRow
	for _, row := range result.Rows {
		if row.Context == "if (x) {" {
			condition = row
		}
	}
	if condition == nil || condition.Type != "string | undefined" {
		t.Errorf("condition row = %+v, want unnarrowed string | undefined", condition)
	}
}

func TestTypeFlowRejectsNonVariable(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/flow.ts": flowSource})
	// `f` is a function, not a variable.
	if _, err := runTypeFlow(context.Background(), ws, &flowFlags{}, []string{"src/flow.ts:1:17"}); err == nil {
		t.Error("expected usage error for a function target")
	}
}

// ---------------------------------------------------------------------------
// type infer

const inferSource = `export function title() {
	return "t";
}

export function double(n) {
	return n * 2;
}

export function pick(v) {
	return v;
}

double(1);
double(2);
pick(3);
pick("x");
`

func TestTypeInfer(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/infer.ts": inferSource})
	result, err := runTypeInfer(context.Background(), ws, &inferFlags{}, []string{"src/infer.ts"})
	if err != nil {
		t.Fatalf("runTypeInfer: %v", err)
	}
	byTarget := make(map[string]*InferSuggestion)
	for _, s := range result.Suggestions {
		byTarget[s.Kind+" "+s.Target] = s
	}

	// (a) Unannotated return of a string literal suggests string.
	title := byTarget["return title"]
	if title == nil {
		t.Fatalf("no return suggestion for title: %+v", result.Suggestions)
	}
	if title.Suggested != "string" || title.Confidence != "high" || title.Current != "none" {
		t.Errorf("title suggestion = %+v, want string/high/none", title)
	}

	// (b) Param called twice with numbers: high-confidence number.
	n := byTarget["param n"]
	if n == nil {
		t.Fatalf("no param suggestion for n: %+v", result.Suggestions)
	}
	if n.Suggested != "number" || n.Confidence != "high" || n.Current != "implicit-any" {
		t.Errorf("n suggestion = %+v, want number/high/implicit-any", n)
	}

	// Param called with number and string: medium-confidence union.
	v := byTarget["param v"]
	if v == nil {
		t.Fatalf("no param suggestion for v: %+v", result.Suggestions)
	}
	if v.Suggested != "number | string" && v.Suggested != "string | number" {
		t.Errorf("v suggestion = %q, want a number/string union", v.Suggested)
	}
	if v.Confidence != "medium" {
		t.Errorf("v confidence = %q, want medium", v.Confidence)
	}
}

func TestTypeInferFixPlanAppliesCleanly(t *testing.T) {
	t.Parallel()
	source := inferSource
	ws := newTestWorkspace(t, map[string]any{"/project/src/infer.ts": source})
	result, err := runTypeInfer(context.Background(), ws, &inferFlags{fixPlan: true}, []string{"src/infer.ts"})
	if err != nil {
		t.Fatalf("runTypeInfer: %v", err)
	}
	if result.FixPlan == nil || len(result.FixPlan.Edits) != 1 {
		t.Fatalf("fix plan = %+v, want edits for one file", result.FixPlan)
	}
	fileEdits := result.FixPlan.Edits[0]
	if fileEdits.File != "src/infer.ts" {
		t.Fatalf("fix plan file = %q", fileEdits.File)
	}

	// Apply the byte-offset insertions back-to-front (the documented
	// apply-edits schema: pos/end byte offsets, newText).
	text := source
	for i := len(fileEdits.Edits) - 1; i >= 0; i-- {
		edit := fileEdits.Edits[i]
		if edit.Pos != edit.End {
			t.Fatalf("annotation edit should be a pure insertion, got %+v", edit)
		}
		if edit.Pos < 0 || edit.Pos > len(text) {
			t.Fatalf("edit out of bounds: %+v", edit)
		}
		text = text[:edit.Pos] + edit.NewText + text[edit.Pos:]
	}
	for _, want := range []string{
		"export function title(): string {",
		"export function double(n: number): number {",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("patched text missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "pick(v: number | string)") && !strings.Contains(text, "pick(v: string | number)") {
		t.Errorf("patched text missing union annotation for v:\n%s", text)
	}

	// The patched file must type-check without the implicit-any errors.
	wsPatched := newTestWorkspace(t, map[string]any{"/project/src/infer.ts": text})
	checkResult, err := runCheck(context.Background(), wsPatched, &checkFlags{code: "TS7006"}, nil)
	if err != nil {
		t.Fatalf("runCheck(patched): %v", err)
	}
	if len(checkResult.Diagnostics) != 0 {
		t.Errorf("patched file still has implicit-any diagnostics: %+v", checkResult.Diagnostics)
	}
}

func TestTypeInferSymbolMode(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{"/project/src/infer.ts": inferSource})
	result, err := runTypeInfer(context.Background(), ws, &inferFlags{symbol: "src/infer.ts#pick"}, nil)
	if err != nil {
		t.Fatalf("runTypeInfer(--symbol): %v", err)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Target != "v" {
		t.Errorf("symbol mode suggestions = %+v, want only param v", result.Suggestions)
	}
}
