package perf

import (
	"math"
	"slices"
	"strings"
)

// Report is the full performance picture: every analysis run from a single
// traced compile. HTML rendering lives in the shared report toolkit
// (internal/tsagent/report); this package produces only the data.
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
