package perf

import (
	"fmt"
	"slices"
	"strings"

	tscore "github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/scanner"
)

// ---- summary ----

// Summary is the project-level performance budget and verdict.
type Summary struct {
	Stats                 Stats    `json:"stats"`
	ParsePct              float64  `json:"parsePct"`
	BindPct               float64  `json:"bindPct"`
	CheckPct              float64  `json:"checkPct"`
	EmitPct               float64  `json:"emitPct"`
	TypesPerFile          float64  `json:"typesPerFile"`
	InstantiationsPerType float64  `json:"instantiationsPerType"`
	DepthLimitHits        int      `json:"depthLimitHits"`
	Verdict               string   `json:"verdict"`
	Notes                 []string `json:"notes,omitempty"`
}

// Summary computes the phase budget and a short diagnostic verdict.
func (c *Capture) Summary() *Summary {
	s := &Summary{Stats: c.Stats, DepthLimitHits: c.depthLimitCount()}
	total := c.Stats.Total.Seconds()
	if total > 0 {
		s.ParsePct = pct(c.Stats.Parse.Seconds(), total)
		s.BindPct = pct(c.Stats.Bind.Seconds(), total)
		s.CheckPct = pct(c.Stats.Check.Seconds(), total)
		s.EmitPct = pct(c.Stats.Emit.Seconds(), total)
	}
	if c.Stats.Files > 0 {
		s.TypesPerFile = float64(c.Stats.Types) / float64(c.Stats.Files)
	}
	if c.Stats.Types > 0 {
		s.InstantiationsPerType = float64(c.Stats.Instantiations) / float64(c.Stats.Types)
	}

	// Verdict: which phase dominates.
	switch {
	case s.CheckPct >= 55:
		s.Verdict = "check-dominated — the type system is the bottleneck"
	case s.ParsePct >= 45:
		s.Verdict = "parse-dominated — I/O and program graph dominate; look at include scope and barrels"
	case s.BindPct >= 30:
		s.Verdict = "bind-dominated — unusually large/many declarations per file"
	case s.EmitPct >= 35:
		s.Verdict = "emit-dominated — declaration emit is expensive; consider explicit return types"
	default:
		s.Verdict = "balanced"
	}

	if s.DepthLimitHits > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d depth/size-limit guard(s) fired — see `perf depth-limits` (these are near-pathological types)", s.DepthLimitHits))
	}
	if s.InstantiationsPerType >= 1.5 {
		s.Notes = append(s.Notes, fmt.Sprintf("high re-instantiation ratio (%.2f instantiations per type) — generics are being re-instantiated a lot; see `perf hot-types`", s.InstantiationsPerType))
	}
	if s.TypesPerFile >= 400 {
		s.Notes = append(s.Notes, fmt.Sprintf("%.0f types/file on average — see `perf hot-files` for where the type system spends its time", s.TypesPerFile))
	}
	return s
}

func (c *Capture) depthLimitCount() int {
	if c.summaryOnly {
		return c.depthLimitHits
	}
	return len(c.Instants)
}

// ---- hot files ----

// HotFile is the per-file time + type attribution.
type HotFile struct {
	Path    string  `json:"path"`
	ParseMS float64 `json:"parseMs"`
	BindMS  float64 `json:"bindMs"`
	CheckMS float64 `json:"checkMs"`
	TotalMS float64 `json:"totalMs"`
	Types   int     `json:"types"`
}

// HotFiles attributes parse/bind/check time and recorded-type count per file,
// ranked by total time descending. Unless includeLibs is set, bundled lib and
// node_modules files (unavoidable baseline cost) are excluded so the ranking
// reflects the user's own codebase.
func (c *Capture) HotFiles(includeLibs bool) []*HotFile {
	byPath := map[string]*HotFile{}
	get := func(p string) *HotFile {
		canon := c.canonical(p)
		if !includeLibs && !c.inProject(canon) {
			return nil
		}
		hf := byPath[canon]
		if hf == nil {
			hf = &HotFile{Path: c.display(canon)}
			byPath[canon] = hf
		}
		return hf
	}
	for _, sp := range c.Spans {
		if sp.Path == "" {
			continue
		}
		hf := get(sp.Path)
		if hf == nil {
			continue
		}
		switch sp.Name {
		case "createSourceFile":
			hf.ParseMS += sp.DurUS / 1000
		case "bindSourceFile":
			hf.BindMS += sp.DurUS / 1000
		case "checkSourceFile":
			hf.CheckMS += sp.DurUS / 1000
		}
	}
	for canon, count := range c.typeCounts {
		if hf := byPath[canon]; hf != nil {
			hf.Types = count
		}
	}
	out := make([]*HotFile, 0, len(byPath))
	for _, hf := range byPath {
		hf.TotalMS = hf.ParseMS + hf.BindMS + hf.CheckMS
		out = append(out, hf)
	}
	slices.SortFunc(out, func(a, b *HotFile) int {
		if a.TotalMS != b.TotalMS {
			return cmpDescF(a.TotalMS, b.TotalMS)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

// ---- hot types ----

// HotType groups recorded type descriptors by their originating symbol, a proxy
// for how widely a generic is instantiated.
type HotType struct {
	Symbol      string `json:"symbol"`
	Count       int    `json:"count"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Conditional int    `json:"conditional"`
	MaxUnion    int    `json:"maxUnionWidth"`
}

// HotTypes ranks the symbols with the most recorded type descriptors. A high
// count means that generic/alias is instantiated in many distinct shapes.
// Unless includeLibs is set, only types declared in the user's own files are
// counted.
func (c *Capture) HotTypes(includeLibs bool) []*HotType {
	byName := c.hotTypesProject
	if includeLibs && c.hotTypesAll != nil {
		byName = c.hotTypesAll
	}
	out := make([]*HotType, 0, len(byName))
	for _, a := range byName {
		out = append(out, a.ht)
	}
	slices.SortFunc(out, func(x, y *HotType) int {
		if x.Count != y.Count {
			return y.Count - x.Count
		}
		return strings.Compare(x.Symbol, y.Symbol)
	})
	return out
}

// ---- hot checks ----

// HotCheck is a single expensive (sampled) checker span.
type HotCheck struct {
	Name  string  `json:"name"`
	Phase string  `json:"phase"`
	DurMS float64 `json:"durMs"`
	File  string  `json:"file,omitempty"`
	Line  int     `json:"line,omitempty"`
}

// HotChecks lists the individual checker operations the tracer sampled as
// slow (>~10ms), ranked by duration. Empty on fast projects, which is itself a
// useful signal. Relation operations carry type ids rather than a path; those
// are attributed to the declaring file of the involved type where possible.
func (c *Capture) HotChecks() []*HotCheck {
	var out []*HotCheck
	for _, sp := range c.Spans {
		if !sp.Sampled {
			continue
		}
		hc := &HotCheck{Name: sp.Name, Phase: sp.Phase, DurMS: sp.DurUS / 1000}
		if sp.Path != "" {
			hc.File = c.display(c.canonical(sp.Path))
			if sp.Pos > 0 {
				hc.Line = c.lineOf(sp.Path, sp.Pos)
			}
		} else if sp.Args != nil {
			if loc := c.fileOfTypeArgs(*sp.Args); loc != "" {
				hc.File = loc
			}
		}
		out = append(out, hc)
	}
	slices.SortFunc(out, func(a, b *HotCheck) int {
		if a.DurMS != b.DurMS {
			return cmpDescF(a.DurMS, b.DurMS)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// ---- depth limits ----

// DepthLimit is a near-pathological guard the checker tripped: a type explosion
// that hit an instantiation/recursion/union-size ceiling.
type DepthLimit struct {
	Name   string `json:"name"`
	Phase  string `json:"phase"`
	File   string `json:"file,omitempty"`
	Detail string `json:"detail"`
}

// DepthLimits surfaces every depth/size-limit guard that fired, resolving the
// implicated type back to its declaring file where possible. These are the
// highest-value findings: each is a concrete, fixable type blow-up.
func (c *Capture) DepthLimits() []*DepthLimit {
	var out []*DepthLimit
	for _, in := range c.Instants {
		dl := &DepthLimit{Name: in.Name, Phase: in.Phase, Detail: formatArgs(in.Args)}
		dl.File = c.fileOfTypeArgs(in.Args)
		out = append(out, dl)
	}
	slices.SortFunc(out, func(a, b *DepthLimit) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.File, b.File)
	})
	return out
}

// ---- helpers ----

func (c *Capture) canonical(p string) string {
	if c.canonMap == nil {
		c.initCanonMap()
	}
	if canon, ok := c.canonMap[p]; ok {
		return canon
	}
	return p
}

func (c *Capture) initCanonMap() {
	if c.canonMap == nil {
		c.canonMap = map[string]string{}
	}
	if c.program == nil {
		return
	}
	for _, f := range c.program.SourceFiles() {
		fn := f.FileName()
		c.canonMap[fn] = fn
		c.canonMap[string(f.Path())] = fn
	}
}

// inProject reports whether a file belongs to the user's own codebase, i.e. it
// is not a bundled lib, not outside the project root, and not under
// node_modules.
func (c *Capture) inProject(canon string) bool {
	d := c.display(canon)
	if strings.HasPrefix(d, "bundled:") || strings.HasPrefix(d, "..") {
		return false
	}
	if strings.Contains(d, "node_modules/") {
		return false
	}
	return true
}

// fileOfTypeArgs resolves the declaring file of the first type id present in a
// trace event's args (typeId/sourceId/targetId), for events that reference
// types rather than a source path.
func (c *Capture) fileOfTypeArgs(args traceArgs) string {
	for _, id := range []uint32{args.TypeID, args.SourceID, args.TargetID} {
		if id == 0 {
			continue
		}
		if idx, ok := c.typeOrigins[id]; ok && idx > 0 && int(idx) <= len(c.originTable) {
			origin := c.originTable[idx-1]
			return c.display(origin.canon)
		}
	}
	return ""
}

func (c *Capture) display(canon string) string {
	if c.ws != nil {
		return c.ws.RelPath(canon)
	}
	return canon
}

func (c *Capture) lineOf(path string, pos int) int {
	if c.ws == nil || pos <= 0 {
		return 0
	}
	canon := c.canonical(path)
	if c.lineStarts == nil {
		c.lineStarts = map[string]tscore.ECMALineStarts{}
	}
	lineStarts, ok := c.lineStarts[canon]
	if !ok {
		text, found := c.ws.FS.ReadFile(canon)
		if !found && canon != path {
			text, found = c.ws.FS.ReadFile(path)
		}
		if !found {
			c.lineStarts[canon] = nil
			return 0
		}
		lineStarts = tscore.ComputeECMALineStarts(text)
		c.lineStarts[canon] = lineStarts
	}
	if len(lineStarts) == 0 {
		return 0
	}
	return scanner.ComputeLineOfPosition(lineStarts, pos) + 1
}

func pct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return part / total * 100
}

func cmpDescF(a, b float64) int {
	if a < b {
		return 1
	}
	if a > b {
		return -1
	}
	return 0
}

func formatArgs(args traceArgs) string {
	var parts []string
	appendStringArg(&parts, "path", args.Path)
	appendIntArg(&parts, "pos", args.Pos)
	appendIntArg(&parts, "end", args.End)
	appendIntArg(&parts, "kind", args.Kind)
	appendUintArg(&parts, "typeId", args.TypeID)
	appendUintArg(&parts, "sourceId", args.SourceID)
	appendUintArg(&parts, "targetId", args.TargetID)
	appendIntArg(&parts, "instantiationDepth", args.InstantiationDepth)
	appendIntArg(&parts, "instantiationCount", args.InstantiationCount)
	appendIntArg(&parts, "estimatedCount", args.EstimatedCount)
	appendIntArg(&parts, "size", args.Size)
	appendIntArg(&parts, "depth", args.Depth)
	appendIntArg(&parts, "targetDepth", args.TargetDepth)
	appendIntArg(&parts, "numCombinations", args.NumCombinations)
	appendIntArg(&parts, "sourceSize", args.SourceSize)
	appendIntArg(&parts, "targetSize", args.TargetSize)
	appendUintArg(&parts, "parent", args.Parent)
	appendUintArg(&parts, "id", args.ID)
	appendIntArg(&parts, "arity", args.Arity)
	appendIntArg(&parts, "checkerId", args.CheckerID)
	return strings.Join(parts, " ")
}

func appendStringArg(parts *[]string, key string, value string) {
	if value != "" {
		*parts = append(*parts, fmt.Sprintf("%s=%s", key, value))
	}
}

func appendIntArg(parts *[]string, key string, value int) {
	if value != 0 {
		*parts = append(*parts, fmt.Sprintf("%s=%d", key, value))
	}
}

func appendUintArg(parts *[]string, key string, value uint32) {
	if value != 0 {
		*parts = append(*parts, fmt.Sprintf("%s=%d", key, value))
	}
}
