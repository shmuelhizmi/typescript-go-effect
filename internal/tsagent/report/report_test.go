package report

import (
	"strings"
	"testing"
)

func TestRenderMultiPageDocument(t *testing.T) {
	doc := &Document{
		Title: "tsagent report",
		Brand: "project",
		Pages: []*Page{
			{ID: "a", Label: "Alpha", Group: "G1", Badge: "2", Body: []Section{
				Hero("Total", "1.5", "s", Tag{Tone: "ok", Word: "ok", Text: "looks fine"}),
				KPIs(KPI{Key: "Files", Value: "8"}),
				// A cell whose text contains markup must be escaped.
				Table("Things", "none", []Col{{Header: "Name"}}, []Row{{{Text: "<b>x</b>", Class: "path"}}}),
			}},
			{ID: "b", Label: "Beta", Group: "G2", Body: []Section{
				Treemap("Map", "sized by value", &TreeNode{Children: []*TreeNode{
					{Name: "f.ts", Path: "f.ts", Value: 10, Tip: []TipKV{{K: "lines", V: "10"}}},
				}}),
			}},
		},
	}
	html := Render(doc)

	for _, want := range []string{
		"<!DOCTYPE html>", "</html>", `class="navlist"`, `class="navgrp"`,
		`class="page active"`, `id="a"`, `id="b"`,
		`data-page="a"`, `data-page="b"`,
		"function tsRenderTreemap(rootId", `id="treemap-b-1"`, `tsRenderTreemap("treemap-b-1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}

	// Page switching, not the old scroll-spy.
	if strings.Contains(html, "IntersectionObserver") {
		t.Error("rendered HTML still uses scroll-spy")
	}
	if !strings.Contains(html, "hashchange") {
		t.Error("rendered HTML missing hash-routed page switching")
	}

	// Cell text is escaped; the raw markup must not appear.
	if !strings.Contains(html, "&lt;b&gt;x&lt;/b&gt;") {
		t.Error("cell text was not HTML-escaped")
	}
	if strings.Contains(html, "<b>x</b>") {
		t.Error("unescaped cell markup leaked into the document")
	}

	// The treemap library is inlined exactly once even with one treemap.
	if n := strings.Count(html, "function tsRenderTreemap(rootId"); n != 1 {
		t.Errorf("treemap library inlined %d times, want 1", n)
	}
}

func TestRenderTreemapAssetOmittedWhenUnused(t *testing.T) {
	doc := &Document{Title: "t", Pages: []*Page{
		{ID: "a", Label: "A", Body: []Section{Empty("nothing here", true)}},
	}}
	html := Render(doc)
	if strings.Contains(html, "function tsRenderTreemap") {
		t.Error("treemap library inlined despite no treemap sections")
	}
}

func TestTableEmptyState(t *testing.T) {
	doc := &Document{Title: "t", Pages: []*Page{
		{ID: "a", Label: "A", Body: []Section{Table("Empty", "no rows here", []Col{{Header: "X"}}, nil)}},
	}}
	html := Render(doc)
	if !strings.Contains(html, "no rows here") {
		t.Error("empty table did not render its empty-state message")
	}
	if strings.Contains(html, "<tbody>") {
		t.Error("empty table rendered a table body")
	}
}
