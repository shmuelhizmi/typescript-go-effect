package perf

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"slices"
	"strings"
)

// Report is the full performance picture: every analysis run from a single
// traced compile.
type Report struct {
	Summary     *Summary      `json:"summary"`
	FileTree    *TreeNode     `json:"fileTree"`
	HotFiles    []*HotFile    `json:"hotFiles"`
	HotTypes    []*HotType    `json:"hotTypes"`
	HotChecks   []*HotCheck   `json:"hotChecks"`
	DepthLimits []*DepthLimit `json:"depthLimits"`
	IncludeLibs bool          `json:"includeLibs"`
	Top         int           `json:"top"`
}

// TreeNode is a folder or file in the project, with compile time aggregated up
// the tree. JSON keys are short because the whole tree is embedded in the HTML
// report and fed to the treemap.
type TreeNode struct {
	Name     string      `json:"n"`
	Path     string      `json:"path,omitempty"`
	TotalMS  float64     `json:"t"`
	ParseMS  float64     `json:"p"`
	BindMS   float64     `json:"b"`
	CheckMS  float64     `json:"c"`
	Types    int         `json:"y"`
	Children []*TreeNode `json:"ch,omitempty"`
}

// Report runs every analysis over the capture and assembles the full report.
// top bounds the ranking tables (0 = unbounded); the treemap always covers all
// files.
func (c *Capture) Report(includeLibs bool, top int) *Report {
	all := c.HotFiles(includeLibs)
	return &Report{
		Summary:     c.Summary(),
		FileTree:    buildTree(all),
		HotFiles:    topN(all, top),
		HotTypes:    topN(c.HotTypes(includeLibs), top),
		HotChecks:   topN(c.HotChecks(), top),
		DepthLimits: c.DepthLimits(),
		IncludeLibs: includeLibs,
		Top:         top,
	}
}

func topN[T any](s []T, n int) []T {
	if n > 0 && len(s) > n {
		return s[:n]
	}
	return s
}

// buildTree turns the flat per-file list into a folder hierarchy, summing time
// into ancestor folders and collapsing single-child folder chains.
func buildTree(files []*HotFile) *TreeNode {
	root := &TreeNode{Name: ""}
	index := map[string]*TreeNode{"": root}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		prefix := ""
		parent := root
		for i, p := range parts {
			if p == "" {
				continue
			}
			if prefix == "" {
				prefix = p
			} else {
				prefix += "/" + p
			}
			node := index[prefix]
			if node == nil {
				node = &TreeNode{Name: p}
				index[prefix] = node
				parent.Children = append(parent.Children, node)
			}
			if i == len(parts)-1 {
				node.Path = f.Path
				node.ParseMS, node.BindMS, node.CheckMS, node.TotalMS, node.Types =
					f.ParseMS, f.BindMS, f.CheckMS, f.TotalMS, f.Types
			}
			parent = node
		}
	}
	aggregate(root)
	collapse(root)
	round(root)
	slices.SortFunc(root.Children, byTotalDesc) // sort handled recursively in aggregate
	return root
}

func aggregate(n *TreeNode) {
	if len(n.Children) == 0 {
		return
	}
	var p, b, c, t float64
	var y int
	for _, ch := range n.Children {
		aggregate(ch)
		p += ch.ParseMS
		b += ch.BindMS
		c += ch.CheckMS
		t += ch.TotalMS
		y += ch.Types
	}
	n.ParseMS, n.BindMS, n.CheckMS, n.TotalMS, n.Types = p, b, c, t, y
	slices.SortFunc(n.Children, byTotalDesc)
}

// collapse merges a folder that has exactly one child folder into that child,
// joining names (e.g. src/main/java -> a single node) for a tidier treemap.
func collapse(n *TreeNode) {
	for len(n.Children) == 1 && len(n.Children[0].Children) > 0 && n.Name != "" {
		child := n.Children[0]
		n.Name += "/" + child.Name
		n.Children = child.Children
	}
	for _, ch := range n.Children {
		collapse(ch)
	}
}

func round(n *TreeNode) {
	n.ParseMS = r2(n.ParseMS)
	n.BindMS = r2(n.BindMS)
	n.CheckMS = r2(n.CheckMS)
	n.TotalMS = r2(n.TotalMS)
	for _, ch := range n.Children {
		round(ch)
	}
}

func r2(x float64) float64 { return math.Round(x*100) / 100 }

func byTotalDesc(a, b *TreeNode) int {
	if a.TotalMS != b.TotalMS {
		if a.TotalMS < b.TotalMS {
			return 1
		}
		return -1
	}
	return strings.Compare(a.Name, b.Name)
}

// ---- HTML rendering ----

// RenderHTML produces a standalone, self-contained HTML report (inline CSS/JS,
// no external assets or network requests).
func RenderHTML(r *Report) string {
	var b strings.Builder
	s := r.Summary
	st := s.Stats

	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>perf report</title><style>`)
	b.WriteString(reportCSS)
	b.WriteString(`</style></head><body>`)

	scope := "project files"
	if r.IncludeLibs {
		scope = "project + libs"
	}

	// Shell: sticky sidebar nav + main content.
	b.WriteString(`<div class="shell"><nav class="nav"><div class="brand"><span class="logo"></span><div><div class="title">Performance</div><div class="proj">` + scope + `</div></div></div><ul class="navlist">`)
	navItem(&b, "overview", "Overview", "")
	if len(r.DepthLimits) > 0 {
		navItem(&b, "issues", "Issues", compact(len(r.DepthLimits)))
	}
	navItem(&b, "wtg", "Where time goes", "")
	navItem(&b, "files", "Hottest files", "")
	navItem(&b, "types", "Instantiations", "")
	navItem(&b, "checks", "Slow checks", "")
	b.WriteString(`</ul></nav><main class="main">`)

	// Overview: total time + verdict, KPIs, phase split.
	b.WriteString(`<section id="overview"><div class="hero"><div class="hero-l"><div class="eyebrow">Total compile time</div>`)
	fmt.Fprintf(&b, `<div class="hero-time"><span class="num">%s</span><span class="unit">s</span></div></div>`, trimZeros(st.Total.Seconds()))
	fmt.Fprintf(&b, `<div class="verdict"><span class="tag tag-%s">%s</span><span>%s</span></div></div>`,
		verdictTone(s), verdictWord(s), html.EscapeString(s.Verdict))

	b.WriteString(`<div class="kpis">`)
	kpi(&b, "Files", compact(st.Files), "")
	kpi(&b, "Types", compact(st.Types), "")
	kpi(&b, "Instantiations", compact(st.Instantiations), "")
	kpi(&b, "Inst / type", fmt.Sprintf("%.1f", s.InstantiationsPerType), warnIf(s.InstantiationsPerType >= 1.5))
	kpi(&b, "Peak memory", fmt.Sprintf("%d", st.MemoryUsed/(1024*1024)), "MB")
	b.WriteString(`</div>`)

	// Phase breakdown (zero phases are skipped to avoid noise).
	b.WriteString(`<div class="phase"><div class="bar">`)
	phaseSeg(&b, "parse", s.ParsePct, "var(--c-parse)")
	phaseSeg(&b, "bind", s.BindPct, "var(--c-bind)")
	phaseSeg(&b, "check", s.CheckPct, "var(--c-check)")
	phaseSeg(&b, "emit", s.EmitPct, "var(--c-emit)")
	b.WriteString(`</div><div class="legend">`)
	legendItem(&b, "parse", "var(--c-parse)", st.Parse.Seconds(), s.ParsePct)
	legendItem(&b, "bind", "var(--c-bind)", st.Bind.Seconds(), s.BindPct)
	legendItem(&b, "check", "var(--c-check)", st.Check.Seconds(), s.CheckPct)
	legendItem(&b, "emit", "var(--c-emit)", st.Emit.Seconds(), s.EmitPct)
	b.WriteString(`</div></div></section>`)

	// Critical issues banner (grouped depth-limit guards).
	if len(r.DepthLimits) > 0 {
		writeIssues(&b, r.DepthLimits)
	}

	// Treemap — the centerpiece.
	b.WriteString(`<section id="wtg" class="block"><div class="head"><h2>Where time goes</h2><p class="sub">Every file sized by total compile time, colored by intensity. Hover for detail.</p></div>`)
	b.WriteString(`<div id="treemap"></div><div class="tm-scale"><span>cool</span><i class="grad"></i><span>hot</span></div></section>`)

	// Ranking tables.
	writeHotFiles(&b, r.HotFiles)
	writeHotTypes(&b, r.HotTypes)
	writeHotChecks(&b, r.HotChecks)

	b.WriteString(`<footer>Generated by <b>tsagent perf</b> — one traced compile, measured in memory.</footer>`)
	b.WriteString(`</main></div>`)

	// Treemap data + scripts.
	data, _ := json.Marshal(r.FileTree)
	b.WriteString(`<script>const TREE=`)
	b.Write(data)
	b.WriteString(`;</script><script>`)
	b.WriteString(treemapJS)
	b.WriteString(`</script><script>`)
	b.WriteString(navJS)
	b.WriteString(`</script></body></html>`)
	return b.String()
}

func writeIssues(b *strings.Builder, limits []*DepthLimit) {
	counts := map[string]int{}
	order := []string{}
	for _, d := range limits {
		if counts[d.Name] == 0 {
			order = append(order, d.Name)
		}
		counts[d.Name]++
	}
	slices.SortFunc(order, func(a, c string) int { return counts[c] - counts[a] })

	b.WriteString(`<section id="issues" class="block issues"><div class="head"><h2 class="crit">`)
	fmt.Fprintf(b, `%d type-explosion guards fired</h2><p class="sub">The checker hit recursion / instantiation / union-size ceilings. Each is a near-pathological type worth simplifying.</p></div><div class="issue-grid">`, len(limits))
	for _, name := range order {
		fmt.Fprintf(b, `<div class="issue"><div class="issue-n">%d</div><div class="issue-name">%s</div><div class="issue-why">%s</div></div>`,
			counts[name], html.EscapeString(prettyGuard(name)), html.EscapeString(guardWhy(name)))
	}
	b.WriteString(`</div></section>`)
}

func writeHotFiles(b *strings.Builder, files []*HotFile) {
	tableHead(b, "files", "Hottest files", len(files))
	if len(files) == 0 {
		b.WriteString(`<div class="empty">No files attributed.</div></section>`)
		return
	}
	maxTotal := 0.0
	for _, f := range files {
		maxTotal = math.Max(maxTotal, f.TotalMS)
	}
	b.WriteString(`<div class="tbl"><table><thead><tr><th>File</th><th class="r">Total</th><th class="r">Check</th><th class="r">Types</th></tr></thead><tbody>`)
	for _, f := range files {
		b.WriteString(`<tr><td class="path">` + html.EscapeString(f.Path) + `</td><td class="r">`)
		meter(b, fmt.Sprintf("%.1f ms", f.TotalMS), f.TotalMS, maxTotal, "var(--c-hot)")
		fmt.Fprintf(b, `</td><td class="r mono dim">%.1f</td><td class="r mono">%d</td></tr>`, f.CheckMS, f.Types)
	}
	b.WriteString(`</tbody></table></div></section>`)
}

func writeHotTypes(b *strings.Builder, types []*HotType) {
	tableHead(b, "types", "Most-instantiated types", len(types))
	if len(types) == 0 {
		b.WriteString(`<div class="empty">No types attributed.</div></section>`)
		return
	}
	maxCount := 0
	for _, t := range types {
		if t.Count > maxCount {
			maxCount = t.Count
		}
	}
	b.WriteString(`<div class="tbl"><table><thead><tr><th>Symbol</th><th class="r">Instantiations</th><th>Origin</th></tr></thead><tbody>`)
	for _, t := range types {
		origin := t.File
		if t.Line > 0 {
			origin = fmt.Sprintf("%s:%d", t.File, t.Line)
		}
		b.WriteString(`<tr><td class="sym">` + html.EscapeString(t.Symbol) + `</td><td class="r">`)
		meter(b, fmt.Sprintf("%d", t.Count), float64(t.Count), float64(maxCount), "var(--c-accent2)")
		fmt.Fprintf(b, `</td><td class="path dim">%s</td></tr>`, html.EscapeString(origin))
	}
	b.WriteString(`</tbody></table></div></section>`)
}

func writeHotChecks(b *strings.Builder, checks []*HotCheck) {
	tableHead(b, "checks", "Slowest checker operations", len(checks))
	if len(checks) == 0 {
		b.WriteString(`<div class="empty"><span class="ok">✓</span> Nothing sampled above ~10ms — the type system is fast.</div></section>`)
		return
	}
	maxDur := 0.0
	for _, c := range checks {
		maxDur = math.Max(maxDur, c.DurMS)
	}
	b.WriteString(`<div class="tbl"><table><thead><tr><th>Operation</th><th class="r">Duration</th><th>Location</th></tr></thead><tbody>`)
	for _, c := range checks {
		loc := c.File
		if c.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.File, c.Line)
		}
		if loc == "" {
			loc = "—"
		}
		b.WriteString(`<tr><td class="sym">` + html.EscapeString(c.Name) + `</td><td class="r">`)
		meter(b, fmt.Sprintf("%.1f ms", c.DurMS), c.DurMS, maxDur, "var(--c-hot)")
		fmt.Fprintf(b, `</td><td class="path dim">%s</td></tr>`, html.EscapeString(loc))
	}
	b.WriteString(`</tbody></table></div></section>`)
}

// ---- small helpers ----

func tableHead(b *strings.Builder, id, title string, n int) {
	fmt.Fprintf(b, `<section id="%s" class="block"><div class="head"><h2>%s<span class="cnt">%d</span></h2></div>`, id, title, n)
}

func navItem(b *strings.Builder, id, label, badge string) {
	bd := ""
	if badge != "" {
		bd = ` <span class="badge">` + badge + `</span>`
	}
	fmt.Fprintf(b, `<li><a href="#%s">%s%s</a></li>`, id, label, bd)
}

func kpi(b *strings.Builder, k, v, unit string) {
	cls := "kpi"
	if unit == "WARN" {
		cls = "kpi warn"
		unit = ""
	}
	u := ""
	if unit != "" {
		u = ` <span class="ku">` + unit + `</span>`
	}
	fmt.Fprintf(b, `<div class="%s"><div class="kk">%s</div><div class="kv">%s%s</div></div>`, cls, k, v, u)
}

func warnIf(cond bool) string {
	if cond {
		return "WARN"
	}
	return ""
}

func legendItem(b *strings.Builder, name, color string, secs, pctVal float64) {
	if pctVal <= 0 {
		return
	}
	fmt.Fprintf(b, `<span class="li"><i style="background:%s"></i>%s <b>%.3fs</b></span>`, color, name, secs)
}

func phaseSeg(b *strings.Builder, name string, pctVal float64, color string) {
	if pctVal <= 0 {
		return
	}
	label := ""
	if pctVal >= 8 {
		label = fmt.Sprintf("%s %.0f%%", name, pctVal)
	}
	fmt.Fprintf(b, `<span style="width:%.4f%%;background:%s">%s</span>`, pctVal, color, label)
}

func meter(b *strings.Builder, text string, val, maxVal float64, color string) {
	w := 0.0
	if maxVal > 0 {
		w = val / maxVal * 100
	}
	fmt.Fprintf(b, `<div class="meter"><div class="fill" style="width:%.2f%%;background:%s"></div><span class="mt">%s</span></div>`,
		w, color, html.EscapeString(text))
}

func compact(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func trimZeros(secs float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", secs), "0"), ".")
}

func verdictWord(s *Summary) string {
	switch {
	case s.CheckPct >= 55:
		return "check"
	case s.ParsePct >= 45:
		return "parse"
	case s.BindPct >= 30:
		return "bind"
	case s.EmitPct >= 35:
		return "emit"
	}
	return "balanced"
}

func verdictTone(s *Summary) string {
	if s.DepthLimitHits > 0 || s.InstantiationsPerType >= 1.5 {
		return "warn"
	}
	return "ok"
}

func prettyGuard(name string) string {
	return strings.TrimSuffix(name, "_DepthLimit")
}

func guardWhy(name string) string {
	switch name {
	case "checkCrossProductUnion_DepthLimit":
		return "Union cross-product too large — discriminate or narrow the unions."
	case "recursiveTypeRelatedTo_DepthLimit", "checkTypeRelatedTo_DepthLimit":
		return "Recursive type comparison too deep — flatten or break the recursion."
	case "instantiateType_DepthLimit":
		return "Type instantiation too deep / too many — simplify the generic."
	case "removeSubtypes_DepthLimit":
		return "Union subtype reduction too large — fewer union members."
	case "getTypeAtFlowNode_DepthLimit":
		return "Control-flow narrowing too deep — simplify the function."
	case "typeRelatedToDiscriminatedType_DepthLimit":
		return "Discriminated-union match too large — reduce variants."
	case "traceUnionsOrIntersectionsTooLarge_DepthLimit":
		return "Union/intersection too large in a relation."
	}
	return "Type complexity ceiling hit."
}
