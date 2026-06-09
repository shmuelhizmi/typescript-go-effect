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
