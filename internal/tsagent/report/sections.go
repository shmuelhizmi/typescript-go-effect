package report

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file defines the concrete Section types and their constructors.
//
// Escaping contract: every *text* field passed to a constructor (labels, cell
// text, file paths, symbol names, tip values) is treated as untrusted and
// escaped at render time. *Class names* and *CSS colors* (Cell.Class,
// MeterSpec.Color, Phase.Color, …) are trusted, provider-controlled constants
// and are emitted verbatim. HTML(raw) is the explicit escape hatch for trusted
// markup.

// ---- Hero ----

// Tag is the small colored verdict chip in a Hero. Tone is "ok" | "warn".
type Tag struct{ Tone, Word, Text string }

// Hero is a headline measurement: a big value + unit and an optional verdict
// tag. An empty Tag.Word omits the chip.
func Hero(eyebrow, value, unit string, tag Tag) Section {
	return &heroSection{eyebrow, value, unit, tag}
}

type heroSection struct {
	eyebrow, value, unit string
	tag                  Tag
}

func (h *heroSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<div class="hero"><div class="hero-l"><div class="eyebrow">` + esc(h.eyebrow) + `</div>`)
	b.WriteString(`<div class="hero-time"><span class="num">` + esc(h.value) + `</span><span class="unit">` + esc(h.unit) + `</span></div></div>`)
	if h.tag.Word != "" || h.tag.Text != "" {
		tone := h.tag.Tone
		if tone == "" {
			tone = "ok"
		}
		b.WriteString(`<div class="verdict"><span class="tag tag-` + tone + `">` + esc(h.tag.Word) + `</span><span>` + esc(h.tag.Text) + `</span></div>`)
	}
	b.WriteString(`</div>`)
}

// ---- KPIs ----

// KPI is one metric card. Warn tints the card.
type KPI struct {
	Key, Value, Unit string
	Warn             bool
}

// KPIs renders a row of metric cards.
func KPIs(cards ...KPI) Section { return &kpiSection{cards} }

type kpiSection struct{ cards []KPI }

func (k *kpiSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<div class="kpis">`)
	for _, c := range k.cards {
		cls := "kpi"
		if c.Warn {
			cls = "kpi warn"
		}
		u := ""
		if c.Unit != "" {
			u = ` <span class="ku">` + esc(c.Unit) + `</span>`
		}
		b.WriteString(`<div class="` + cls + `"><div class="kk">` + esc(c.Key) + `</div><div class="kv">` + esc(c.Value) + u + `</div></div>`)
	}
	b.WriteString(`</div>`)
}

// ---- PhaseBar ----

// Phase is one segment of a proportion bar. Color is a trusted constant.
type Phase struct {
	Name, Color  string
	Pct, Seconds float64
}

// PhaseBar renders a segmented proportion bar plus a legend. Zero-Pct segments
// are skipped.
func PhaseBar(segs ...Phase) Section { return &phaseSection{segs} }

type phaseSection struct{ segs []Phase }

func (p *phaseSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<div class="phase"><div class="bar">`)
	for _, s := range p.segs {
		if s.Pct <= 0 {
			continue
		}
		label := ""
		if s.Pct >= 8 {
			label = fmt.Sprintf("%s %.0f%%", s.Name, s.Pct)
		}
		fmt.Fprintf(b, `<span style="width:%.4f%%;background:%s">%s</span>`, s.Pct, s.Color, esc(label))
	}
	b.WriteString(`</div><div class="legend">`)
	for _, s := range p.segs {
		if s.Pct <= 0 {
			continue
		}
		fmt.Fprintf(b, `<span class="li"><i style="background:%s"></i>%s <b>%.3fs</b></span>`, s.Color, esc(s.Name), s.Seconds)
	}
	b.WriteString(`</div></div>`)
}

// ---- Table ----

// Col is a table column header. Right right-aligns the column.
type Col struct {
	Header string
	Right  bool
	Class  string // optional trusted extra class on the <th>
}

// Row is one table row.
type Row []Cell

// Cell is one table cell. Text is escaped; Class is a trusted set of classes.
// When Meter is set, an inline proportional bar is drawn behind Text.
type Cell struct {
	Text  string
	Class string
	Meter *MeterSpec
}

// MeterSpec is an inline bar behind a cell's text (Val of Max, colored Color).
type MeterSpec struct {
	Val, Max float64
	Color    string
}

// Table renders a ranking/measurement table. When rows is empty it renders the
// emptyMsg as a neutral empty-state instead.
func Table(title, emptyMsg string, cols []Col, rows []Row) Section {
	return &tableSection{title, emptyMsg, cols, rows}
}

type tableSection struct {
	title, emptyMsg string
	cols            []Col
	rows            []Row
}

func (t *tableSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<section class="block">`)
	blockHead(b, t.title, len(t.rows), true, "")
	if len(t.rows) == 0 {
		(&emptySection{t.emptyMsg, false}).renderHTML(b)
		b.WriteString(`</section>`)
		return
	}
	b.WriteString(`<div class="tbl"><table><thead><tr>`)
	for _, c := range t.cols {
		cls := c.Class
		if c.Right {
			if cls != "" {
				cls += " "
			}
			cls += "r"
		}
		if cls != "" {
			b.WriteString(`<th class="` + cls + `">` + esc(c.Header) + `</th>`)
		} else {
			b.WriteString(`<th>` + esc(c.Header) + `</th>`)
		}
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range t.rows {
		b.WriteString(`<tr>`)
		for _, cell := range row {
			if cell.Class != "" {
				b.WriteString(`<td class="` + cell.Class + `">`)
			} else {
				b.WriteString(`<td>`)
			}
			if cell.Meter != nil {
				meter(b, cell.Text, cell.Meter.Val, cell.Meter.Max, cell.Meter.Color)
			} else {
				b.WriteString(esc(cell.Text))
			}
			b.WriteString(`</td>`)
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div></section>`)
}

// ---- IssueGrid ----

// Issue is one card in an IssueGrid (a counted, named finding).
type Issue struct {
	Count     int
	Name, Why string
}

// IssueGrid renders the emphasized "findings" grid. It is the one widget with a
// critical (red) treatment; use it only for genuinely pathological data.
func IssueGrid(title, sub string, items ...Issue) Section {
	return &issueSection{title, sub, items}
}

type issueSection struct {
	title, sub string
	items      []Issue
}

func (s *issueSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<section class="block issues"><div class="head"><h2 class="crit">` + esc(s.title) + `</h2>`)
	if s.sub != "" {
		b.WriteString(`<p class="sub">` + esc(s.sub) + `</p>`)
	}
	b.WriteString(`</div><div class="issue-grid">`)
	for _, it := range s.items {
		fmt.Fprintf(b, `<div class="issue"><div class="issue-n">%d</div><div class="issue-name">%s</div><div class="issue-why">%s</div></div>`,
			it.Count, esc(it.Name), esc(it.Why))
	}
	b.WriteString(`</div></section>`)
}

// ---- Empty ----

// Empty renders a neutral empty-state card. ok=true adds a green check mark.
func Empty(msg string, ok bool) Section { return &emptySection{msg, ok} }

type emptySection struct {
	msg string
	ok  bool
}

func (e *emptySection) renderHTML(b *strings.Builder) {
	b.WriteString(`<div class="empty">`)
	if e.ok {
		b.WriteString(`<span class="ok">✓</span> `)
	}
	b.WriteString(esc(e.msg) + `</div>`)
}

// ---- Treemap ----

// TipKV is one row of a treemap node's hover tooltip.
type TipKV struct {
	K string `json:"k"`
	V string `json:"v"`
}

// TreeNode is a node in a treemap heatmap. Cells are sized by Value; colored by
// Heat (0..1) when set, otherwise by Value relative to the largest leaf Value.
// This lets one widget serve both "size and color by the same metric" (Heat
// nil) and "size by one metric, color by another" (Heat set).
type TreeNode struct {
	Name     string      `json:"n"`
	Path     string      `json:"path,omitempty"`
	Value    float64     `json:"v"`
	Heat     *float64    `json:"h,omitempty"`
	Tip      []TipKV     `json:"tip,omitempty"`
	Children []*TreeNode `json:"ch,omitempty"`
}

// Heat is a convenience for the optional Heat color override.
func Heat(f float64) *float64 { return &f }

// Treemap renders a squarified treemap heatmap of the given tree. Tip values
// are escaped client-side via textContent; Name/Path likewise.
func Treemap(title, sub string, root *TreeNode) Section {
	return &treemapSection{title: title, sub: sub, root: root}
}

type treemapSection struct {
	title, sub string
	root       *TreeNode
	id         string // assigned by Render
}

func (t *treemapSection) assetKeys() []string { return []string{"treemap"} }

func (t *treemapSection) renderHTML(b *strings.Builder) {
	b.WriteString(`<section class="block">`)
	blockHead(b, t.title, 0, false, t.sub)
	b.WriteString(`<div id="` + t.id + `" class="treemap"></div>`)
	b.WriteString(`<div class="tm-scale"><span>cool</span><i class="grad"></i><span>hot</span></div></section>`)
	data, _ := json.Marshal(t.root)
	idJSON, _ := json.Marshal(t.id)
	b.WriteString(`<script>tsRenderTreemap(` + string(idJSON) + `,`)
	b.Write(data)
	b.WriteString(`)</script>`)
}

// ---- HTML escape hatch ----

// HTML emits trusted raw markup verbatim. Use sparingly.
func HTML(raw string) Section { return rawSection(raw) }

type rawSection string

func (r rawSection) renderHTML(b *strings.Builder) { b.WriteString(string(r)) }
