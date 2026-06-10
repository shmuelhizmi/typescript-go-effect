package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// SchemaVersion is the version stamped on every JSON result.
const SchemaVersion = 1

// Format selects the output encoding.
type Format string

const (
	FormatJSON   Format = "json"
	FormatText   Format = "text"
	FormatNDJSON Format = "ndjson"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatJSON, FormatText, FormatNDJSON:
		return Format(s), nil
	case "":
		return FormatJSON, nil
	}
	return "", UsageErrorf("invalid --format %q (want json, text, or ndjson)", s)
}

// Lister is implemented by list-shaped results. The output layer applies
// --limit/--offset windowing generically and always reports truncation.
type Lister interface {
	// Total is the full number of items before windowing.
	Total() int
	// Item returns the i-th item (0 <= i < Total).
	Item(i int) any
	// WriteItemText writes the compact text form of one item.
	WriteItemText(w io.Writer, item any) error
}

// Texter is implemented by non-list results that have a compact text form.
type Texter interface {
	WriteText(w io.Writer) error
}

// ZeroTexter lets a Lister customize the line text mode prints when the
// result has no items at all (default: "0 results"). Empty results are never
// silent — an agent must be able to tell "nothing found" from "no output".
type ZeroTexter interface {
	ZeroText() string
}

// Output writes command results in the configured format.
type Output struct {
	W      io.Writer
	Format Format
	Limit  int // 0 means unlimited
	Offset int
}

type listEnvelope struct {
	SchemaVersion int   `json:"schemaVersion"`
	Total         int   `json:"total"`
	Offset        int   `json:"offset"`
	Count         int   `json:"count"`
	Truncated     bool  `json:"truncated"`
	Items         []any `json:"items"`
}

type resultEnvelope struct {
	SchemaVersion int `json:"schemaVersion"`
	Result        any `json:"result"`
}

// Write renders the result. List-shaped results (implementing Lister) are
// windowed by Offset/Limit; truncation is always reported, never silent.
func (o *Output) Write(result any) error {
	if list, ok := result.(Lister); ok {
		return o.writeList(list)
	}
	switch o.Format {
	case FormatText:
		if t, ok := result.(Texter); ok {
			return t.WriteText(o.W)
		}
		// Pre-encoded JSON results (daemon admin replies routed through
		// `serve status|stop|reload|overlay|snapshot`) get the generic
		// compact text rendering instead of a JSON dump.
		if raw, ok := result.(json.RawMessage); ok {
			return writeRawJSONText(o.W, raw)
		}
		return o.writeJSON(resultEnvelope{SchemaVersion: SchemaVersion, Result: result})
	default:
		return o.writeJSON(resultEnvelope{SchemaVersion: SchemaVersion, Result: result})
	}
}

func (o *Output) window(list Lister) (items []any, truncated bool) {
	total := list.Total()
	start := min(max(o.Offset, 0), total)
	end := total
	if o.Limit > 0 {
		end = min(start+o.Limit, total)
	}
	items = make([]any, 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, list.Item(i))
	}
	return items, start > 0 || end < total
}

func (o *Output) writeList(list Lister) error {
	items, truncated := o.window(list)
	switch o.Format {
	case FormatNDJSON:
		enc := json.NewEncoder(o.W)
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
		return enc.Encode(listEnvelope{
			SchemaVersion: SchemaVersion,
			Total:         list.Total(),
			Offset:        o.Offset,
			Count:         len(items),
			Truncated:     truncated,
			Items:         []any{},
		})
	case FormatText:
		if list.Total() == 0 {
			zero := "0 results"
			if z, ok := list.(ZeroTexter); ok {
				zero = z.ZeroText()
			}
			_, err := fmt.Fprintln(o.W, zero)
			return err
		}
		for _, item := range items {
			if err := list.WriteItemText(o.W, item); err != nil {
				return err
			}
		}
		if truncated {
			fmt.Fprintf(o.W, "(showing %d of %d results; use --limit/--offset to page)\n", len(items), list.Total())
		}
		return nil
	default:
		return o.writeJSON(listEnvelope{
			SchemaVersion: SchemaVersion,
			Total:         list.Total(),
			Offset:        o.Offset,
			Count:         len(items),
			Truncated:     truncated,
			Items:         items,
		})
	}
}

func (o *Output) writeJSON(v any) error {
	enc := json.NewEncoder(o.W)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---------------------------------------------------------------------------
// Generic text rendering of pre-encoded JSON

// writeRawJSONText renders an arbitrary JSON value as compact text: objects
// become one `key: value` line per field (insertion order preserved), nested
// composites indent one level, arrays print one element per line (all-scalar
// objects inline as `k: v  k: v`), and empty composites print `(none)`.
func writeRawJSONText(w io.Writer, raw json.RawMessage) error {
	return writeJSONTextValue(w, raw, 0)
}

func writeJSONTextValue(w io.Writer, raw json.RawMessage, depth int) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '{':
		pairs, err := decodeOrderedObject(raw)
		if err != nil {
			return err
		}
		if len(pairs) == 0 {
			_, err := fmt.Fprintf(w, "%s(none)\n", Indent(depth))
			return err
		}
		for _, p := range pairs {
			if isCompositeJSON(p.value) && !isEmptyCompositeJSON(p.value) {
				if _, err := fmt.Fprintf(w, "%s%s:\n", Indent(depth), p.key); err != nil {
					return err
				}
				if err := writeJSONTextValue(w, p.value, depth+1); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "%s%s: %s\n", Indent(depth), p.key, jsonScalarText(p.value)); err != nil {
				return err
			}
		}
		return nil
	case '[':
		elems, err := decodeJSONArray(raw)
		if err != nil {
			return err
		}
		if len(elems) == 0 {
			_, err := fmt.Fprintf(w, "%s(none)\n", Indent(depth))
			return err
		}
		for _, e := range elems {
			if line, ok := inlineObjectText(e); ok {
				if _, err := fmt.Fprintf(w, "%s%s\n", Indent(depth), line); err != nil {
					return err
				}
				continue
			}
			if isCompositeJSON(e) {
				if err := writeJSONTextValue(w, e, depth); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "%s%s\n", Indent(depth), jsonScalarText(e)); err != nil {
				return err
			}
		}
		return nil
	default:
		_, err := fmt.Fprintf(w, "%s%s\n", Indent(depth), jsonScalarText(raw))
		return err
	}
}

type jsonPair struct {
	key   string
	value json.RawMessage
}

// decodeOrderedObject decodes a JSON object preserving field order (a plain
// map unmarshal would lose it).
func decodeOrderedObject(raw json.RawMessage) ([]jsonPair, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // consume '{'
		return nil, err
	}
	var pairs []jsonPair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		pairs = append(pairs, jsonPair{key: key, value: value})
	}
	return pairs, nil
}

func decodeJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // consume '['
		return nil, err
	}
	var elems []json.RawMessage
	for dec.More() {
		var e json.RawMessage
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		elems = append(elems, e)
	}
	return elems, nil
}

// inlineObjectText renders an all-scalar object as one compact line
// (`file: /a.ts  bytes: 12`); ok is false when the object nests composites.
func inlineObjectText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return "", false
	}
	pairs, err := decodeOrderedObject(raw)
	if err != nil || len(pairs) == 0 {
		return "", false
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		if isCompositeJSON(p.value) {
			return "", false
		}
		parts[i] = p.key + ": " + jsonScalarText(p.value)
	}
	return strings.Join(parts, "  "), true
}

func isCompositeJSON(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
}

func isEmptyCompositeJSON(raw json.RawMessage) bool {
	var v []json.RawMessage
	switch {
	case len(raw) == 0:
		return true
	case raw[0] == '[':
		err := json.Unmarshal(raw, &v)
		return err == nil && len(v) == 0
	case raw[0] == '{':
		var m map[string]json.RawMessage
		err := json.Unmarshal(raw, &m)
		return err == nil && len(m) == 0
	}
	return false
}

// jsonScalarText renders a scalar JSON value: strings unquoted, everything
// else (numbers, booleans, null, and empty composites) verbatim.
func jsonScalarText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if isEmptyCompositeJSON(raw) && (len(raw) > 0 && (raw[0] == '[' || raw[0] == '{')) {
		return "(none)"
	}
	return string(raw)
}
