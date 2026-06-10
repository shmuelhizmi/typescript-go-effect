package cmds

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
)

// analyze duplicates (§4.5): AST-based structural clone detection. Clone
// units are function-like declarations with bodies plus top-level block
// statements. Each unit is serialized into a structural fingerprint
// (pre-order node kinds with identifiers/literals normalized, property names
// kept); units with identical fingerprints form a clone class. v1 detects
// exact-structure (type-2) clones only — near-miss clones with small
// structural diffs are out of scope.

func init() {
	cli.Register(cli.Command{
		Family:       "analyze",
		Name:         "duplicates",
		Summary:      "AST-based structural clone detection (exact-structure clone classes)",
		NeedsProgram: true,
		Flags: func(fs *flag.FlagSet) any {
			f := &duplicatesFlags{}
			fs.IntVar(&f.minNodes, "min-nodes", 25, "minimum AST node count for a clone unit")
			fs.BoolVar(&f.crossFileOnly, "cross-file-only", false, "only report classes spanning at least two files")
			return f
		},
		Run: func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error) {
			return runAnalyzeDuplicates(ctx, ws, flags.(*duplicatesFlags), args)
		},
	})
}

type duplicatesFlags struct {
	minNodes      int
	crossFileOnly bool
}

// CloneMember is one occurrence of a clone class.
type CloneMember struct {
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Name      string `json:"name,omitempty"`
}

// CloneClass groups structurally identical clone units.
type CloneClass struct {
	NodeCount  int            `json:"nodeCount"`
	Similarity string         `json:"similarity"` // exact-structure (v1)
	Members    []*CloneMember `json:"members"`
}

// DuplicatesResult is the `analyze duplicates` result, classes ranked by
// (member count × node count) descending.
type DuplicatesResult struct {
	Classes []*CloneClass `json:"classes"`
	Note    string        `json:"note,omitempty"`
}

var _ cli.Texter = (*DuplicatesResult)(nil)

func (r *DuplicatesResult) WriteText(w io.Writer) error {
	for i, class := range r.Classes {
		if _, err := fmt.Fprintf(w, "clone class %d: %d members, %d nodes (%s)\n", i+1, len(class.Members), class.NodeCount, class.Similarity); err != nil {
			return err
		}
		for _, m := range class.Members {
			name := m.Name
			if name == "" {
				name = "(block)"
			}
			if _, err := fmt.Fprintf(w, "  %s:%d-%d  %s\n", m.File, m.StartLine, m.EndLine, name); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "total: %d clone class(es)\n", len(r.Classes))
	return err
}

// cloneUnit is one candidate subtree with its structural fingerprint.
type cloneUnit struct {
	file        *ast.SourceFile
	node        *ast.Node
	name        string
	fingerprint string
	hash        uint64
	nodeCount   int
}

func runAnalyzeDuplicates(ctx context.Context, ws *core.Workspace, flags *duplicatesFlags, args []string) (*DuplicatesResult, error) {
	files, err := projectFiles(ws, args)
	if err != nil {
		return nil, err
	}

	var units []*cloneUnit
	for _, file := range files {
		for _, fn := range functionLikeNodesWithBody(file) {
			units = append(units, newCloneUnit(file, fn, functionDisplayName(fn)))
		}
		for _, statement := range file.Statements.Nodes {
			if statement.Kind == ast.KindBlock {
				units = append(units, newCloneUnit(file, statement, ""))
			}
		}
	}

	// Group by hash, then verify full fingerprints inside each bucket so a
	// 64-bit collision can never merge structurally different code.
	byHash := make(map[uint64][]*cloneUnit)
	for _, unit := range units {
		if unit.nodeCount < flags.minNodes {
			continue
		}
		byHash[unit.hash] = append(byHash[unit.hash], unit)
	}

	result := &DuplicatesResult{
		Classes: []*CloneClass{},
		Note:    "v1 reports exact-structure (type-2) clones only; near-miss clones with small structural diffs are not detected",
	}
	for _, bucket := range byHash {
		if len(bucket) < 2 {
			continue
		}
		byFingerprint := make(map[string][]*cloneUnit)
		for _, unit := range bucket {
			byFingerprint[unit.fingerprint] = append(byFingerprint[unit.fingerprint], unit)
		}
		for _, members := range byFingerprint {
			if len(members) < 2 {
				continue
			}
			if flags.crossFileOnly && !spansMultipleFiles(members) {
				continue
			}
			class := &CloneClass{NodeCount: members[0].nodeCount, Similarity: "exact-structure", Members: []*CloneMember{}}
			for _, unit := range members {
				startLine, _ := ws.PosToLineCol(unit.file, astnav.GetStartOfNode(unit.node, unit.file, false /*includeJSDoc*/))
				endLine, _ := ws.PosToLineCol(unit.file, unit.node.End())
				class.Members = append(class.Members, &CloneMember{
					File:      ws.RelPath(unit.file.FileName()),
					StartLine: startLine,
					EndLine:   endLine,
					Name:      unit.name,
				})
			}
			slices.SortFunc(class.Members, func(a, b *CloneMember) int {
				if c := strings.Compare(a.File, b.File); c != 0 {
					return c
				}
				return a.StartLine - b.StartLine
			})
			result.Classes = append(result.Classes, class)
		}
	}

	slices.SortFunc(result.Classes, func(a, b *CloneClass) int {
		if d := len(b.Members)*b.NodeCount - len(a.Members)*a.NodeCount; d != 0 {
			return d
		}
		if c := strings.Compare(a.Members[0].File, b.Members[0].File); c != 0 {
			return c
		}
		return a.Members[0].StartLine - b.Members[0].StartLine
	})
	return result, nil
}

func spansMultipleFiles(units []*cloneUnit) bool {
	for _, unit := range units[1:] {
		if unit.file != units[0].file {
			return true
		}
	}
	return false
}

func newCloneUnit(file *ast.SourceFile, node *ast.Node, name string) *cloneUnit {
	fingerprint, count := structuralFingerprint(node)
	h := fnv.New64a()
	_, _ = h.Write([]byte(fingerprint))
	return &cloneUnit{
		file:        file,
		node:        node,
		name:        name,
		fingerprint: fingerprint,
		hash:        h.Sum64(),
		nodeCount:   count,
	}
}

// structuralFingerprint serializes a subtree into a parenthesized pre-order
// string of node kinds and returns it with the subtree's node count.
// Normalization: identifiers (including the unit's own name and private
// identifiers) become ID; string/template/regex literals become STR/TMPL;
// numeric and bigint literals become NUM. Property names are kept verbatim
// (prefixed P:) because they are semantic: property-access and object-literal
// member names distinguish otherwise identical shapes.
func structuralFingerprint(root *ast.Node) (string, int) {
	var sb strings.Builder
	count := 0
	var visit func(node *ast.Node)
	visit = func(node *ast.Node) {
		count++
		sb.WriteByte('(')
		sb.WriteString(fingerprintToken(node))
		node.ForEachChild(func(child *ast.Node) bool {
			visit(child)
			return false
		})
		sb.WriteByte(')')
	}
	visit(root)
	return sb.String(), count
}

func fingerprintToken(node *ast.Node) string {
	switch node.Kind {
	case ast.KindIdentifier, ast.KindPrivateIdentifier:
		if isPropertyNamePosition(node) {
			return "P:" + node.Text()
		}
		return "ID"
	case ast.KindStringLiteral, ast.KindRegularExpressionLiteral:
		return "STR"
	case ast.KindNoSubstitutionTemplateLiteral, ast.KindTemplateHead, ast.KindTemplateMiddle, ast.KindTemplateTail:
		return "TMPL"
	case ast.KindNumericLiteral, ast.KindBigIntLiteral:
		return "NUM"
	}
	return strconv.Itoa(int(node.Kind))
}

// isPropertyNamePosition reports whether an identifier is a semantic member
// name: the .name of a property access / qualified name, or the declared name
// of an object-literal / class / interface member.
func isPropertyNamePosition(node *ast.Node) bool {
	parent := node.Parent
	if parent == nil {
		return false
	}
	switch parent.Kind {
	case ast.KindPropertyAccessExpression:
		return parent.AsPropertyAccessExpression().Name() == node
	case ast.KindQualifiedName:
		return parent.AsQualifiedName().Right == node
	case ast.KindPropertyAssignment, ast.KindShorthandPropertyAssignment,
		ast.KindPropertyDeclaration, ast.KindPropertySignature,
		ast.KindMethodDeclaration, ast.KindMethodSignature,
		ast.KindGetAccessor, ast.KindSetAccessor:
		name := ast.GetNameOfDeclaration(parent)
		return name == node
	}
	return false
}
