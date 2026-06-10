package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

type testItem struct {
	Name string `json:"name"`
}

type testList struct {
	items []*testItem
}

func (l *testList) Total() int     { return len(l.items) }
func (l *testList) Item(i int) any { return l.items[i] }

func (l *testList) WriteItemText(w io.Writer, item any) error {
	_, err := fmt.Fprintf(w, "- %s\n", item.(*testItem).Name)
	return err
}

func newTestList(n int) *testList {
	l := &testList{}
	for i := range n {
		l.items = append(l.items, &testItem{Name: fmt.Sprintf("item%d", i)})
	}
	return l
}

func TestOutputJSONEnvelope(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatJSON}
	if err := out.Write(newTestList(3)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var envelope struct {
		SchemaVersion int               `json:"schemaVersion"`
		Total         int               `json:"total"`
		Truncated     bool              `json:"truncated"`
		Count         int               `json:"count"`
		Items         []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, sb.String())
	}
	if envelope.SchemaVersion != SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", envelope.SchemaVersion, SchemaVersion)
	}
	if envelope.Total != 3 || envelope.Count != 3 || envelope.Truncated {
		t.Errorf("envelope = %+v", envelope)
	}
}

func TestOutputPagination(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatJSON, Limit: 2, Offset: 1}
	if err := out.Write(newTestList(5)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var envelope struct {
		Total     int        `json:"total"`
		Offset    int        `json:"offset"`
		Count     int        `json:"count"`
		Truncated bool       `json:"truncated"`
		Items     []testItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if envelope.Total != 5 || envelope.Count != 2 || !envelope.Truncated || envelope.Offset != 1 {
		t.Errorf("envelope = %+v", envelope)
	}
	if envelope.Items[0].Name != "item1" || envelope.Items[1].Name != "item2" {
		t.Errorf("items = %+v", envelope.Items)
	}

	// Offset past the end yields an empty, truncated window.
	sb.Reset()
	out = &Output{W: &sb, Format: FormatJSON, Offset: 10}
	if err := out.Write(newTestList(3)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := json.Unmarshal([]byte(sb.String()), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if envelope.Count != 0 || !envelope.Truncated {
		t.Errorf("past-the-end envelope = %+v", envelope)
	}
}

func TestOutputText(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatText, Limit: 2}
	if err := out.Write(newTestList(5)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	text := sb.String()
	if !strings.Contains(text, "- item0\n- item1\n") {
		t.Errorf("text output missing items:\n%s", text)
	}
	if !strings.Contains(text, "showing 2 of 5") {
		t.Errorf("truncation must be reported:\n%s", text)
	}
}

// zeroTextList is a Lister with a custom empty-result line.
type zeroTextList struct{ testList }

func (l *zeroTextList) ZeroText() string { return "0 diagnostics" }

func TestOutputTextEmptyListNeverSilent(t *testing.T) {
	t.Parallel()

	// Default zero line.
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatText}
	if err := out.Write(newTestList(0)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sb.String() != "0 results\n" {
		t.Errorf("empty list text = %q, want \"0 results\\n\"", sb.String())
	}

	// ZeroTexter override.
	sb.Reset()
	if err := out.Write(&zeroTextList{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sb.String() != "0 diagnostics\n" {
		t.Errorf("ZeroTexter text = %q, want \"0 diagnostics\\n\"", sb.String())
	}

	// Non-empty lists are unchanged (no zero line), and the JSON formats
	// keep their envelope shape for empty lists.
	sb.Reset()
	if err := out.Write(newTestList(1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sb.String() != "- item0\n" {
		t.Errorf("non-empty list text = %q", sb.String())
	}
	sb.Reset()
	jsonOut := &Output{W: &sb, Format: FormatJSON}
	if err := jsonOut.Write(newTestList(0)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(sb.String(), `"total": 0`) {
		t.Errorf("empty list JSON = %q, want the regular envelope", sb.String())
	}
}

func TestOutputTextRawJSON(t *testing.T) {
	t.Parallel()
	render := func(raw string) string {
		t.Helper()
		var sb strings.Builder
		out := &Output{W: &sb, Format: FormatText}
		if err := out.Write(json.RawMessage(raw)); err != nil {
			t.Fatalf("Write(%s): %v", raw, err)
		}
		return sb.String()
	}

	// Objects render as key: value lines in field order; nested arrays of
	// all-scalar objects inline; empty composites render (none).
	got := render(`{"configPath":"/p/tsconfig.json","overlayCount":0,"snapshots":[],"overlays":[{"file":"src/a.ts","bytes":12}],"ok":true}`)
	want := "configPath: /p/tsconfig.json\noverlayCount: 0\nsnapshots: (none)\noverlays:\n  file: src/a.ts  bytes: 12\nok: true\n"
	if got != want {
		t.Errorf("object text = %q, want %q", got, want)
	}

	if got := render(`{"ok":true}`); got != "ok: true\n" {
		t.Errorf("scalar object text = %q", got)
	}
	if got := render(`[]`); got != "(none)\n" {
		t.Errorf("empty array text = %q", got)
	}
	if got := render(`["a","b"]`); got != "a\nb\n" {
		t.Errorf("scalar array text = %q", got)
	}

	// Non-text formats keep the JSON envelope.
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatJSON}
	if err := out.Write(json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(sb.String(), `"schemaVersion"`) {
		t.Errorf("JSON format must keep the envelope, got %q", sb.String())
	}
}

func TestOutputNDJSON(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatNDJSON}
	if err := out.Write(newTestList(2)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(sb.String()), "\n")
	if len(lines) != 3 { // 2 items + summary
		t.Fatalf("expected 3 ndjson lines, got %d:\n%s", len(lines), sb.String())
	}
	var item testItem
	if err := json.Unmarshal([]byte(lines[0]), &item); err != nil || item.Name != "item0" {
		t.Errorf("first line = %q (err %v)", lines[0], err)
	}
	var summary struct {
		SchemaVersion int `json:"schemaVersion"`
		Total         int `json:"total"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &summary); err != nil || summary.Total != 2 {
		t.Errorf("summary line = %q (err %v)", lines[2], err)
	}
}

func TestOutputNonListResult(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	out := &Output{W: &sb, Format: FormatJSON}
	if err := out.Write(&testItem{Name: "solo"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var envelope struct {
		SchemaVersion int      `json:"schemaVersion"`
		Result        testItem `json:"result"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.Result.Name != "solo" {
		t.Errorf("envelope = %+v", envelope)
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	if got := ExitCode(nil); got != ExitOK {
		t.Errorf("nil → %d, want %d", got, ExitOK)
	}
	if got := ExitCode(UsageErrorf("bad")); got != ExitUsage {
		t.Errorf("usage → %d, want %d", got, ExitUsage)
	}
	if got := ExitCode(NotFoundErrorf("missing")); got != ExitNotFound {
		t.Errorf("not found → %d, want %d", got, ExitNotFound)
	}
	if got := ExitCode(RefusedErrorf("no")); got != ExitRefused {
		t.Errorf("refused → %d, want %d", got, ExitRefused)
	}
	if got := ExitCode(PartialErrorf("some")); got != ExitPartial {
		t.Errorf("partial → %d, want %d", got, ExitPartial)
	}
	if got := ExitCode(fmt.Errorf("boom")); got != ExitFailed {
		t.Errorf("generic → %d, want %d", got, ExitFailed)
	}
	if got := ExitCode(fmt.Errorf("wrap: %w", NotFoundErrorf("inner"))); got != ExitNotFound {
		t.Errorf("wrapped → %d, want %d", got, ExitNotFound)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("no-op truncate: %q", got)
	}
	if got := Truncate("hello world", 5); got != "hello…" {
		t.Errorf("truncate: %q", got)
	}
	// Never split a multi-byte rune.
	if got := Truncate("héllo", 2); got != "h…" {
		t.Errorf("utf8 truncate: %q", got)
	}
}
