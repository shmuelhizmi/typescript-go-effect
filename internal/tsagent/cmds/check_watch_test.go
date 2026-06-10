package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// watchTestWriter buffers the ndjson stream and signals once the "ready"
// event (end of the initial scan) has been written, so the test can edit
// files at a deterministic point.
type watchTestWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	ready     chan struct{}
	readyOnce sync.Once
}

func (w *watchTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), `"event":"ready"`) {
		w.readyOnce.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *watchTestWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestCheckWatchStreamsDelta(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, map[string]any{
		"/project/src/ok.ts":  "export const fine: number = 1;\n",
		"/project/src/bad.ts": "export const broken: string = 42;\n",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := &watchTestWriter{ready: make(chan struct{})}
	flags := &checkWatchFlags{
		interval:       5 * time.Millisecond,
		maxRebuilds:    1,
		out:            out,
		singleThreaded: true,
	}

	// Once the initial scan is done, rewrite the broken file in one atomic
	// write: the old error is fixed and a different one is planted, so one
	// rebuild must stream a fixed + a new event.
	go func() {
		select {
		case <-out.ready:
		case <-ctx.Done():
			return
		}
		_ = ws.FS.WriteFile("/project/src/bad.ts",
			"export const broken: string = \"ok\";\nexport const fresh: number = \"oops!\";\n")
	}()

	if _, err := runCheckWatch(ctx, ws, flags, nil); err != nil {
		t.Fatalf("runCheckWatch: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("watch timed out without completing a rebuild")
	}

	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("non-JSON ndjson line %q: %v", line, err)
		}
		events = append(events, event)
	}

	countBy := func(kind string) []map[string]any {
		var matched []map[string]any
		for _, e := range events {
			if e["event"] == kind {
				matched = append(matched, e)
			}
		}
		return matched
	}

	initial := countBy("initial")
	if len(initial) != 1 || initial[0]["file"] != "src/bad.ts" || initial[0]["code"] != "TS2322" {
		t.Errorf("initial events = %+v, want the planted src/bad.ts TS2322", initial)
	}
	if ready := countBy("ready"); len(ready) != 1 || ready[0]["diagnostics"] != float64(1) {
		t.Errorf("ready events = %+v", ready)
	}
	newEvents := countBy("new")
	if len(newEvents) != 1 || newEvents[0]["file"] != "src/bad.ts" {
		t.Errorf("new events = %+v, want one in src/bad.ts", newEvents)
	}
	fixedEvents := countBy("fixed")
	if len(fixedEvents) != 1 || fixedEvents[0]["file"] != "src/bad.ts" {
		t.Errorf("fixed events = %+v, want one in src/bad.ts", fixedEvents)
	}
	if newEvents[0]["diagRef"] == fixedEvents[0]["diagRef"] {
		t.Error("new and fixed must be different diagnostics")
	}
	summaries := countBy("summary")
	if len(summaries) != 1 {
		t.Fatalf("summary events = %+v, want exactly 1 (max-rebuilds 1)", summaries)
	}
	s := summaries[0]
	if s["new"] != float64(1) || s["fixed"] != float64(1) || s["files"] != float64(1) {
		t.Errorf("summary = %+v, want new:1 fixed:1 files:1", s)
	}
	// Summary must be the last line (one rebuild, then exit).
	if events[len(events)-1]["event"] != "summary" {
		t.Errorf("last event = %+v, want summary", events[len(events)-1])
	}
}
