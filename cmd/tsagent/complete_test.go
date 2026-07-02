package main

import (
	"strings"
	"testing"
)

func hasCand(cands []candidate, value string) bool {
	for _, c := range cands {
		if c.value == value {
			return true
		}
	}
	return false
}

func TestCompleteFamilies(t *testing.T) {
	cands, dir := completeFor(nil, "")
	if dir != dirNoFiles {
		t.Errorf("family completion directive = %d, want %d", dir, dirNoFiles)
	}
	for _, want := range []string{"map", "report", "perf", "refactor", "completion"} {
		if !hasCand(cands, want) {
			t.Errorf("families missing %q", want)
		}
	}
}

func TestCompleteFamilyPrefixFilter(t *testing.T) {
	cands, _ := completeFor(nil, "re")
	if !hasCand(cands, "report") || !hasCand(cands, "refactor") {
		t.Errorf("prefix 're' should match report and refactor, got %v", values(cands))
	}
	if hasCand(cands, "map") {
		t.Errorf("prefix 're' should not match map")
	}
}

func TestCompleteSubcommands(t *testing.T) {
	cands, _ := completeFor([]string{"report"}, "")
	for _, want := range []string{"perf", "quality", "structure", "full"} {
		if !hasCand(cands, want) {
			t.Errorf("report subcommands missing %q (got %v)", want, values(cands))
		}
	}
	// A family with no direct (empty-name) command suppresses file completion in
	// the subcommand position; one that has one (e.g. `check`) still allows files.
	_, dir := completeFor([]string{"map"}, "")
	if dir != dirNoFiles {
		t.Errorf("map subcommand directive = %d, want %d", dir, dirNoFiles)
	}
}

func TestCompleteFlags(t *testing.T) {
	// `report perf -<tab>` → global + report flags.
	cands, dir := completeFor([]string{"report", "perf"}, "-")
	if dir != dirNoFiles {
		t.Errorf("flag directive = %d, want %d", dir, dirNoFiles)
	}
	for _, want := range []string{"--out", "--top", "--project", "--format"} {
		if !hasCand(cands, want) {
			t.Errorf("flags missing %q (got %v)", want, values(cands))
		}
	}
}

func TestCompleteEnumFlagValues(t *testing.T) {
	formats, dir := completeFor([]string{"check", "--format"}, "")
	if dir != dirNoFiles {
		t.Errorf("--format directive = %d, want %d", dir, dirNoFiles)
	}
	for _, want := range []string{"json", "text", "ndjson"} {
		if !hasCand(formats, want) {
			t.Errorf("--format values missing %q", want)
		}
	}
	connects, _ := completeFor([]string{"map", "search", "--connect"}, "")
	for _, want := range []string{"never", "auto", "require"} {
		if !hasCand(connects, want) {
			t.Errorf("--connect values missing %q", want)
		}
	}
}

func TestCompleteIncludeValues(t *testing.T) {
	cands, _ := completeFor([]string{"report", "--include"}, "")
	for _, want := range []string{"perf", "duplicates", "file-size", "dead-code"} {
		if !hasCand(cands, want) {
			t.Errorf("--include values missing %q (got %v)", want, values(cands))
		}
	}
	// Comma-list: complete the tail and re-prefix the already-chosen items.
	tail, _ := completeFor([]string{"report", "--include"}, "perf,du")
	if !hasCand(tail, "perf,duplicates") {
		t.Errorf("comma-list completion should yield perf,duplicates, got %v", values(tail))
	}
}

func TestCompleteInlineFlagValue(t *testing.T) {
	cands, _ := completeFor([]string{"check"}, "--format=js")
	if !hasCand(cands, "--format=json") {
		t.Errorf("inline --format= completion should yield --format=json, got %v", values(cands))
	}
}

func TestCompletePositionalFallsBackToFiles(t *testing.T) {
	_, dir := completeFor([]string{"check"}, "src/")
	if dir != dirDefault {
		t.Errorf("positional directive = %d, want %d (file completion)", dir, dirDefault)
	}
}

func TestCompletionScriptsRegistered(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		s, ok := completionScripts[shell]
		if !ok || !strings.Contains(s, "__complete") {
			t.Errorf("%s completion script missing or does not call __complete", shell)
		}
	}
}

func values(cands []candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.value
	}
	return out
}
