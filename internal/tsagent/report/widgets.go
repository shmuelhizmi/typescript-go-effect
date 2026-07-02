package report

import (
	"fmt"
	"html"
	"strings"
)

// esc escapes untrusted text for HTML body/attribute contexts. Every text
// field a caller passes through a section constructor flows through esc; class
// names and CSS colors are trusted, provider-controlled constants and are
// emitted verbatim.
func esc(s string) string { return html.EscapeString(s) }

// meter writes an inline bar (proportional fill behind right-aligned text),
// reused by table cells. text is escaped; color is a trusted constant.
func meter(b *strings.Builder, text string, val, maxVal float64, color string) {
	w := 0.0
	if maxVal > 0 {
		w = val / maxVal * 100
	}
	fmt.Fprintf(b, `<div class="meter"><div class="fill" style="width:%.2f%%;background:%s"></div><span class="mt">%s</span></div>`,
		w, color, esc(text))
}

// blockHead writes the standard section header (title + optional count badge +
// optional sub line). title/sub are escaped.
func blockHead(b *strings.Builder, title string, count int, hasCount bool, sub string) {
	b.WriteString(`<div class="head"><h2>` + esc(title))
	if hasCount {
		fmt.Fprintf(b, `<span class="cnt">%d</span>`, count)
	}
	b.WriteString(`</h2>`)
	if sub != "" {
		b.WriteString(`<p class="sub">` + esc(sub) + `</p>`)
	}
	b.WriteString(`</div>`)
}
