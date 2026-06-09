package cli

import (
	"encoding/json"
	"fmt"
	"io"
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
