package main

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/microsoft/typescript-go/internal/effectify"
	"github.com/microsoft/typescript-go/internal/glob"
	"github.com/microsoft/typescript-go/internal/tspath"
)

const effectifyUsage = `Usage: tsgo --effectify [options] <file-or-glob>...

Rewrites TypeScript files using the effect library to EffectScript (.ets).
Only .ts files are considered (.tsx/.d.ts/.ets are skipped). By default the
proposed changes are printed as a diff; nothing is written.

Options:
  --write                 apply changes: write <file>.ets and remove <file>.ts
  --check                 print only the summary; exit 1 if anything would convert
  --full                  print the full proposed file contents instead of a diff
  --import-source <name>  effect import source to look for (default "effect")
`

func runEffectify(args []string) int {
	var patterns []string
	write := false
	check := false
	full := false
	importSource := "effect"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			write = true
		case "--check":
			check = true
		case "--full":
			full = true
		case "--import-source":
			i++
			if i >= len(args) {
				fmt.Print(effectifyUsage)
				return 1
			}
			importSource = args[i]
		case "--help", "-h":
			fmt.Print(effectifyUsage)
			return 0
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Printf("unknown option %q\n\n%s", args[i], effectifyUsage)
				return 1
			}
			patterns = append(patterns, args[i])
		}
	}
	if len(patterns) == 0 {
		fmt.Print(effectifyUsage)
		return 1
	}

	sys := newSystem()
	out := sys.Writer()
	files, err := resolveEffectifyFiles(sys, patterns)
	if err != nil {
		fmt.Fprintf(out, "effectify: %v\n", err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintf(out, "effectify: no .ts files matched\n")
		return 1
	}

	converted := 0
	patternsTotal := 0
	skipped := make([][2]string, 0)
	noChange := 0
	for _, file := range files {
		src, ok := sys.FS().ReadFile(file)
		if !ok {
			skipped = append(skipped, [2]string{file, "unreadable"})
			continue
		}
		result := effectify.Effectify(file, src, effectify.Options{ImportSource: importSource})
		switch {
		case result.Converted:
			converted++
			patternsTotal += result.Stats.Total()
			target := strings.TrimSuffix(file, ".ts") + ".ets"
			switch {
			case write:
				if sys.FS().FileExists(target) {
					skipped = append(skipped, [2]string{file, "target-exists"})
					converted--
					patternsTotal -= result.Stats.Total()
					continue
				}
				if err := sys.FS().WriteFile(target, result.Output); err != nil {
					skipped = append(skipped, [2]string{file, "write-failed: " + err.Error()})
					converted--
					patternsTotal -= result.Stats.Total()
					continue
				}
				if err := sys.FS().Remove(file); err != nil {
					fmt.Fprintf(out, "warning: wrote %s but could not remove %s: %v\n", target, file, err)
				}
				fmt.Fprintf(out, "converted %s -> %s (%s)\n", file, target, result.Stats.String())
			case check:
				fmt.Fprintf(out, "would convert %s (%s)\n", file, result.Stats.String())
			case full:
				fmt.Fprintf(out, "--- proposed %s ---\n%s\n", target, result.Output)
			default:
				fmt.Fprintf(out, "--- a/%s\n+++ b/%s\n%s", file, target, lineDiff(src, result.Output))
			}
		case result.SkipReason != "":
			detail := result.SkipReason
			if result.Detail != "" {
				detail += " (" + result.Detail + ")"
			}
			skipped = append(skipped, [2]string{file, detail})
		default:
			noChange++
		}
	}

	fmt.Fprintf(out, "\nscanned %d files, %d converted (%d patterns), %d unchanged, %d skipped\n",
		len(files), converted, patternsTotal, noChange, len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(out, "  skipped %s: %s\n", s[0], s[1])
	}
	if check && converted > 0 {
		return 1
	}
	return 0
}

// resolveEffectifyFiles expands the given paths/globs into a sorted, deduped
// list of candidate .ts files.
func resolveEffectifyFiles(sys *osSys, patterns []string) ([]string, error) {
	seen := make(map[string]bool)
	var files []string
	add := func(path string) {
		if !isEffectifyCandidate(path) || seen[path] {
			return
		}
		seen[path] = true
		files = append(files, path)
	}

	for _, pattern := range patterns {
		abs := tspath.GetNormalizedAbsolutePath(pattern, sys.GetCurrentDirectory())
		if sys.FS().FileExists(abs) {
			if !isEffectifyCandidate(abs) {
				return nil, fmt.Errorf("%s is not a convertible .ts file", pattern)
			}
			add(abs)
			continue
		}
		g, err := glob.Parse(abs)
		if err != nil {
			return nil, fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
		root := globWalkRoot(abs)
		err = sys.FS().WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			if g.Match(path) {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func isEffectifyCandidate(path string) bool {
	return strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".d.ts") &&
		!strings.HasSuffix(path, ".tsx") && !strings.Contains(path, "/node_modules/")
}

// globWalkRoot returns the longest wildcard-free directory prefix of an
// absolute glob, so the walk doesn't start at the filesystem root.
func globWalkRoot(pattern string) string {
	idx := strings.IndexAny(pattern, "*?{[")
	if idx == -1 {
		return tspath.GetDirectoryPath(pattern)
	}
	slash := strings.LastIndex(pattern[:idx], "/")
	if slash <= 0 {
		return "/"
	}
	return pattern[:slash]
}

// lineDiff renders a minimal unified-style diff (no hunk headers) between two
// texts using an LCS over lines.
func lineDiff(a, b string) string {
	al := strings.SplitAfter(a, "\n")
	bl := strings.SplitAfter(b, "\n")
	if len(al) > 0 && al[len(al)-1] == "" {
		al = al[:len(al)-1]
	}
	if len(bl) > 0 && bl[len(bl)-1] == "" {
		bl = bl[:len(bl)-1]
	}

	// LCS table (fine for source-file sized inputs).
	n, m := len(al), len(bl)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var out strings.Builder
	i, j := 0, 0
	writeLine := func(prefix, line string) {
		out.WriteString(prefix)
		out.WriteString(strings.TrimSuffix(line, "\n"))
		out.WriteString("\n")
	}
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			writeLine("-", al[i])
			i++
		default:
			writeLine("+", bl[j])
			j++
		}
	}
	for ; i < n; i++ {
		writeLine("-", al[i])
	}
	for ; j < m; j++ {
		writeLine("+", bl[j])
	}
	return out.String()
}
