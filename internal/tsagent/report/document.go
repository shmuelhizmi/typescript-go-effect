// Package report is a self-contained HTML report toolkit shared across the
// tsagent command families. A report is a multi-page Document: an ordered set
// of Pages, each a switchable view built from reusable Sections (KPIs, ranking
// tables, treemap heatmaps, …). Render produces one standalone HTML string with
// all CSS/JS inlined — no network, no external assets — with hash-routed page
// switching (#pageid deep links).
//
// The toolkit is intentionally measurement-oriented: it renders rankings,
// distributions, and heatmaps. It carries no opinion about pass/fail; callers
// decide what to surface.
package report

import (
	"fmt"
	"strings"
)

// Document is a multi-page, self-contained HTML report.
type Document struct {
	Title string // <title> and sidebar brand title
	Brand string // mono subtitle under the brand (e.g. the project root)
	Pages []*Page
}

// Page is one switchable view = one sidebar entry.
type Page struct {
	ID    string    // url-safe; drives the #deep-link and show/hide
	Label string    // sidebar text
	Badge string    // optional sidebar badge (count/severity); "" = none
	Group string    // optional sidebar grouping header; "" = ungrouped
	Body  []Section // ordered content blocks
}

// Section is one content block. Callers construct sections via the builders in
// sections.go; the rendering method is unexported so the section set stays
// closed to this package.
type Section interface {
	renderHTML(b *strings.Builder)
}

// assetKeyer is implemented by sections that need an extra inlined JS asset
// (e.g. the treemap). Render unions these across all pages and inlines each
// asset at most once.
type assetKeyer interface {
	assetKeys() []string
}

// Render returns a standalone HTML string for the document: inline CSS+JS, no
// network requests, hash-routed page switching, deep links via #pageid.
func Render(doc *Document) string {
	// Decide which optional assets are needed before emitting <head>, so the
	// treemap library is defined before the per-treemap render calls in the
	// page bodies run.
	needs := map[string]bool{}
	for _, p := range doc.Pages {
		for _, s := range p.Body {
			if ak, ok := s.(assetKeyer); ok {
				for _, k := range ak.assetKeys() {
					needs[k] = true
				}
			}
		}
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + esc(doc.Title) + `</title><style>`)
	b.WriteString(reportCSS)
	b.WriteString(`</style>`)
	if needs["treemap"] {
		// Defined in <head> so the inline tsRenderTreemap(...) calls emitted by
		// Treemap sections resolve. The tooltip element is created lazily.
		b.WriteString(`<script>` + treemapLibJS + `</script>`)
	}
	b.WriteString(`</head><body>`)

	// Sidebar: brand + grouped page list.
	b.WriteString(`<div class="shell"><nav class="nav"><div class="brand"><span class="logo"></span><div><div class="title">` +
		esc(doc.Title) + `</div><div class="proj">` + esc(doc.Brand) + `</div></div></div><ul class="navlist">`)
	lastGroup := ""
	for i, p := range doc.Pages {
		if p.Group != lastGroup {
			b.WriteString(`<li class="navgrp">` + esc(p.Group) + `</li>`)
			lastGroup = p.Group
		}
		active := ""
		if i == 0 {
			active = " active"
		}
		badge := ""
		if p.Badge != "" {
			badge = ` <span class="badge">` + esc(p.Badge) + `</span>`
		}
		b.WriteString(`<li><a class="navp` + active + `" href="#` + esc(p.ID) + `" data-page="` + esc(p.ID) + `">` +
			esc(p.Label) + badge + `</a></li>`)
	}
	b.WriteString(`</ul></nav><main class="main">`)

	// Pages: one <section class="page"> each; CSS shows only .page.active.
	for i, p := range doc.Pages {
		active := ""
		if i == 0 {
			active = " active"
		}
		b.WriteString(`<section class="page` + active + `" id="` + esc(p.ID) + `">`)
		tmN := 0
		for _, s := range p.Body {
			if tm, ok := s.(*treemapSection); ok {
				tmN++
				tm.id = fmt.Sprintf("treemap-%s-%d", p.ID, tmN)
			}
			s.renderHTML(&b)
		}
		b.WriteString(`</section>`)
	}

	b.WriteString(`</main></div>`)
	b.WriteString(`<script>` + pageNavJS + `</script>`)
	b.WriteString(`</body></html>`)
	return b.String()
}
