package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// check_watch.go implements `check watch` (spec §4.7): stream diagnostics
// deltas as ndjson while files change. Output deliberately bypasses the
// normal output layer (--format is ignored): every event is one JSON line
// written (and flushed) to stdout as it happens, so an agent can tail the
// stream. The watcher is a simple poll loop over the program files' size and
// mtime; on change the workspace is rebuilt from scratch and the new
// diagnostics snapshot is diffed against the previous one.

func init() {
	cli.Register(cli.Command{
		Family:       "check",
		Name:         "watch",
		Summary:      "Stream diagnostics deltas as ndjson while files change (ignores --format)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &checkWatchFlags{}
			fs.DurationVar(&f.interval, "interval", 2*time.Second, "poll interval")
			fs.IntVar(&f.maxRebuilds, "max-rebuilds", 0, "exit after N rebuilds (0 = run until SIGINT/SIGTERM)")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runCheckWatch(ctx, ws, flags.(*checkWatchFlags), args)
		},
	})
}

type checkWatchFlags struct {
	interval    time.Duration
	maxRebuilds int

	// out overrides the ndjson destination (tests). Defaults to os.Stdout.
	out io.Writer
	// singleThreaded builds rebuild programs single-threaded (tests).
	singleThreaded bool
}

// watchDiagEvent is one streamed diagnostic line.
type watchDiagEvent struct {
	Event string `json:"event"` // initial | new | fixed
	core.SpecDiag
}

// watchSummaryEvent closes each rebuild (and the initial scan).
type watchSummaryEvent struct {
	Event       string `json:"event"` // ready | summary
	New         int    `json:"new"`
	Fixed       int    `json:"fixed"`
	Files       int    `json:"files"`       // changed files triggering the rebuild (ready: program files watched)
	Diagnostics int    `json:"diagnostics"` // current total
}

func runCheckWatch(ctx context.Context, ws *core.Workspace, flags *checkWatchFlags, args []string) (any, error) {
	if len(args) > 0 {
		return nil, cli.UsageErrorf("check watch takes no positional arguments (got %q)", args[0])
	}
	if flags.interval <= 0 {
		return nil, cli.UsageErrorf("--interval must be positive")
	}
	out := flags.out
	if out == nil {
		out = os.Stdout
	}
	enc := json.NewEncoder(out) // one Encode = one flushed ndjson line

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fingerprint before the initial check so edits racing the (potentially
	// slow) first type-check still register as changes on the first poll.
	fingerprints := watchFingerprints(ws)
	snapshot := core.CollectDiagnostics(ctx, ws)
	for _, diag := range snapshot {
		if err := enc.Encode(watchDiagEvent{Event: "initial", SpecDiag: diag}); err != nil {
			return nil, err
		}
	}
	if err := enc.Encode(watchSummaryEvent{Event: "ready", Files: len(fingerprints), Diagnostics: len(snapshot)}); err != nil {
		return nil, err
	}

	ticker := time.NewTicker(flags.interval)
	defer ticker.Stop()
	rebuilds := 0
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-ticker.C:
		}
		changed := watchChangedFiles(ws, fingerprints)
		if changed == 0 {
			continue
		}
		next, err := core.NewWorkspace(core.Options{
			Project:        ws.ConfigPath,
			Cwd:            ws.Cwd,
			FS:             ws.FS,
			SingleThreaded: flags.singleThreaded,
		})
		if err != nil {
			return nil, err
		}
		ws = next
		nextSnapshot := core.CollectDiagnostics(ctx, ws)
		delta := core.DiagnosticsDelta(snapshot, nextSnapshot)
		for _, diag := range delta.New {
			if err := enc.Encode(watchDiagEvent{Event: "new", SpecDiag: diag}); err != nil {
				return nil, err
			}
		}
		for _, diag := range delta.Fixed {
			if err := enc.Encode(watchDiagEvent{Event: "fixed", SpecDiag: diag}); err != nil {
				return nil, err
			}
		}
		summary := watchSummaryEvent{
			Event:       "summary",
			New:         len(delta.New),
			Fixed:       len(delta.Fixed),
			Files:       changed,
			Diagnostics: len(nextSnapshot),
		}
		if err := enc.Encode(summary); err != nil {
			return nil, err
		}
		snapshot = nextSnapshot
		fingerprints = watchFingerprints(ws)
		rebuilds++
		if flags.maxRebuilds > 0 && rebuilds >= flags.maxRebuilds {
			return nil, nil
		}
	}
}

// watchFingerprint identifies a file's state cheaply: mtime + size when the
// FS provides them, content otherwise.
type watchFingerprint struct {
	modTime int64
	size    int64
	content string
}

// watchFingerprints snapshots the current program files (libs excluded).
func watchFingerprints(ws *core.Workspace) map[string]watchFingerprint {
	fingerprints := make(map[string]watchFingerprint)
	for _, file := range ws.Program.SourceFiles() {
		if ws.Program.IsLibFile(file) || strings.Contains(file.FileName(), "/node_modules/") {
			continue
		}
		fingerprints[file.FileName()] = watchFingerprintOf(ws, file.FileName())
	}
	return fingerprints
}

func watchFingerprintOf(ws *core.Workspace, fileName string) watchFingerprint {
	if info := ws.FS.Stat(fileName); info != nil {
		return watchFingerprint{modTime: info.ModTime().UnixNano(), size: info.Size()}
	}
	content, _ := ws.FS.ReadFile(fileName)
	return watchFingerprint{content: content}
}

// watchChangedFiles counts watched files whose fingerprint changed (edits and
// deletions; files added to the program show up after the next rebuild).
func watchChangedFiles(ws *core.Workspace, fingerprints map[string]watchFingerprint) int {
	changed := 0
	for fileName, previous := range fingerprints {
		if !ws.FS.FileExists(fileName) {
			changed++
			continue
		}
		if watchFingerprintOf(ws, fileName) != previous {
			changed++
		}
	}
	return changed
}
