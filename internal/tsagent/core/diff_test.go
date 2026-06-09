package core

import (
	"strings"
	"testing"
)

// roundTrip renders a diff for old→new and re-applies the parsed diff to old,
// asserting the result equals new.
func roundTrip(t *testing.T, name string, old string, new string) {
	t.Helper()
	diff := UnifiedDiff("a/x.ts", "b/x.ts", old, new)
	if old == new {
		if diff != "" {
			t.Errorf("%s: identical texts produced a non-empty diff:\n%s", name, diff)
		}
		return
	}
	if diff == "" {
		t.Fatalf("%s: differing texts produced an empty diff", name)
	}
	patches, err := ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("%s: ParseUnifiedDiff: %v\ndiff:\n%s", name, err, diff)
	}
	patch, ok := patches["x.ts"]
	if !ok {
		t.Fatalf("%s: patch map %v missing x.ts", name, patches)
	}
	applied, err := patch.Apply(old)
	if err != nil {
		t.Fatalf("%s: Apply: %v\ndiff:\n%s", name, err, diff)
	}
	if applied != new {
		t.Errorf("%s: round-trip mismatch\nold:\n%q\nwant:\n%q\ngot:\n%q\ndiff:\n%s", name, old, new, applied, diff)
	}
}

func TestDiffRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"identical", "a\nb\n", "a\nb\n"},
		{"change middle", "a\nb\nc\nd\ne\n", "a\nb\nC\nd\ne\n"},
		{"insert at start", "a\nb\n", "z\na\nb\n"},
		{"insert at end", "a\nb\n", "a\nb\nz\n"},
		{"delete at start", "a\nb\nc\n", "b\nc\n"},
		{"delete at end", "a\nb\nc\n", "a\nb\n"},
		{"replace all", "a\nb\n", "x\ny\nz\n"},
		{"two distant hunks", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n", "1\nTWO\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\nNINETEEN\n20\n"},
		{"nearby hunks merge", "1\n2\n3\n4\n5\n6\n7\n8\n", "1\nB\n3\n4\n5\nF\n7\n8\n"},
		{"no trailing newline old", "a\nb", "a\nb\nc\n"},
		{"no trailing newline new", "a\nb\n", "a\nb\nc"},
		{"no trailing newline both", "a\nb", "a\nc"},
		{"newline-only change", "a\nb", "a\nb\n"},
		{"empty to content", "", "hello\nworld\n"},
		{"content to empty", "hello\nworld\n", ""},
		{"empty lines", "a\n\n\nb\n", "a\n\nb\n"},
	}
	for _, tc := range cases {
		roundTrip(t, tc.name, tc.old, tc.new)
	}
}

func TestDiffRenderShape(t *testing.T) {
	t.Parallel()
	diff := UnifiedDiff("a/src/f.ts", "b/src/f.ts", "const a = 1;\nconst b = 2;\n", "const a = 1;\nconst b = 3;\n")
	wantLines := []string{
		"--- a/src/f.ts",
		"+++ b/src/f.ts",
		"@@ -1,2 +1,2 @@",
		" const a = 1;",
		"-const b = 2;",
		"+const b = 3;",
	}
	got := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if len(got) != len(wantLines) {
		t.Fatalf("diff line count = %d, want %d:\n%s", len(got), len(wantLines), diff)
	}
	for i, want := range wantLines {
		if got[i] != want {
			t.Errorf("line %d = %q, want %q", i+1, got[i], want)
		}
	}
}

func TestDiffParseMultiFile(t *testing.T) {
	t.Parallel()
	diffA := UnifiedDiff("a/a.ts", "b/a.ts", "1\n", "one\n")
	diffNew := UnifiedDiff("/dev/null", "b/fresh.ts", "", "export {};\n")
	diffGone := UnifiedDiff("a/gone.ts", "/dev/null", "dead\n", "")
	patches, err := ParseUnifiedDiff(diffA + diffNew + diffGone)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("got %d patches, want 3: %v", len(patches), patches)
	}
	if p := patches["a.ts"]; p == nil || p.IsNew || p.IsDelete {
		t.Errorf("a.ts patch = %+v, want plain edit", p)
	}
	if p := patches["fresh.ts"]; p == nil || !p.IsNew {
		t.Errorf("fresh.ts patch = %+v, want IsNew", p)
	} else if applied, err := p.Apply(""); err != nil || applied != "export {};\n" {
		t.Errorf("fresh.ts apply = %q, %v", applied, err)
	}
	if p := patches["gone.ts"]; p == nil || !p.IsDelete {
		t.Errorf("gone.ts patch = %+v, want IsDelete", p)
	} else if applied, err := p.Apply("dead\n"); err != nil || applied != "" {
		t.Errorf("gone.ts apply = %q, %v", applied, err)
	}
}

func TestDiffApplyContextMismatch(t *testing.T) {
	t.Parallel()
	diff := UnifiedDiff("a/x.ts", "b/x.ts", "a\nb\nc\n", "a\nB\nc\n")
	patches, err := ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if _, err := patches["x.ts"].Apply("completely\ndifferent\ncontent\n"); err == nil {
		t.Error("expected a context mismatch error")
	}
}
