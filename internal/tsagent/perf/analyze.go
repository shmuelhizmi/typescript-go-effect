package perf

import (
	"fmt"
	"slices"
	"strings"

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
	s := &Summary{Stats: c.Stats, DepthLimitHits: len(c.Instants)}
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
	for i := range c.Types {
		t := &c.Types[i]
		if t.FirstDeclaration == nil {
			continue
		}
		canon := c.canonical(t.FirstDeclaration.Path)
		if hf := byPath[canon]; hf != nil {
			hf.Types++
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
	type agg struct {
		ht        *HotType
		haveOrign bool
	}
	byName := map[string]*agg{}
	for i := range c.Types {
		t := &c.Types[i]
		if !includeLibs {
			if t.FirstDeclaration == nil || !c.inProject(c.canonical(t.FirstDeclaration.Path)) {
				continue
			}
		}
		name := t.SymbolName
		if name == "" {
			name = t.IntrinsicName
		}
		if name == "" {
			continue
		}
		a := byName[name]
		if a == nil {
			a = &agg{ht: &HotType{Symbol: name}}
			byName[name] = a
		}
		a.ht.Count++
		if len(t.UnionTypes) > a.ht.MaxUnion {
			a.ht.MaxUnion = len(t.UnionTypes)
		}
		if slices.Contains(t.Flags, "Conditional") {
			a.ht.Conditional++
		}
		if !a.haveOrign && t.FirstDeclaration != nil {
			a.ht.File = c.display(c.canonical(t.FirstDeclaration.Path))
			if t.FirstDeclaration.Start != nil {
				a.ht.Line = t.FirstDeclaration.Start.Line
			}
			a.haveOrign = true
		}
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
		} else if loc := c.fileOfTypeArgs(sp.Args); loc != "" {
			hc.File = loc
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
		c.canonMap = map[string]string{}
		if c.program != nil {
			for _, f := range c.program.SourceFiles() {
				fn := f.FileName()
				c.canonMap[fn] = fn
				c.canonMap[string(f.Path())] = fn
			}
		}
	}
	if canon, ok := c.canonMap[p]; ok {
		return canon
	}
	return p
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
func (c *Capture) fileOfTypeArgs(args map[string]any) string {
	for _, key := range []string{"typeId", "sourceId", "targetId"} {
		if id, ok := argUint(args, key); ok {
			if t := c.TypeByID[id]; t != nil && t.FirstDeclaration != nil {
				return c.display(c.canonical(t.FirstDeclaration.Path))
			}
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
	if c.program == nil {
		return 0
	}
	file := c.program.GetSourceFile(c.canonical(path))
	if file == nil {
		file = c.program.GetSourceFile(path)
	}
	if file == nil {
		return 0
	}
	line, _ := scanner.GetECMALineAndUTF16CharacterOfPosition(file, pos)
	return line + 1
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

func argUint(args map[string]any, key string) (uint32, bool) {
	switch v := args[key].(type) {
	case float64:
		return uint32(v), true
	case int:
		return uint32(v), true
	}
	return 0, false
}

func formatArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, formatArgValue(args[k])))
	}
	return strings.Join(parts, " ")
}

func formatArgValue(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}
