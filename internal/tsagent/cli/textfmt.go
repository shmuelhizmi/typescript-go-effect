package cli

import "strings"

// Indent returns two spaces per depth level for the compact text outline format.
func Indent(depth int) string {
	return strings.Repeat("  ", depth)
}

// Truncate caps s at max bytes, appending an ellipsis when cut. It never
// splits a UTF-8 sequence.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// CommaSet parses a comma-separated flag value into a set; empty input
// returns nil (meaning "no filter").
func CommaSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	set := make(map[string]bool)
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			set[part] = true
		}
	}
	return set
}
