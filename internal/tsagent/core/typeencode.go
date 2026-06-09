package core

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/checker"
)

// TypeDisplayFlags are the format flags used for every type display string in
// tsagent output: the checker's TypeToString defaults plus NoTruncation so
// agents never receive elided types.
const TypeDisplayFlags = checker.TypeFormatFlagsNoTruncation |
	checker.TypeFormatFlagsAllowUniqueESSymbolType |
	checker.TypeFormatFlagsUseAliasDefinedOutsideCurrentScope

// TypeJSON is the structural JSON form of a type. Recursive fields
// (unionMembers, intersectionMembers, typeArguments) are depth-limited;
// beyond the limit only display/flags/kind are populated.
type TypeJSON struct {
	Display             string             `json:"display"`
	Flags               []string           `json:"flags"`
	Kind                string             `json:"kind"`
	AliasSymbol         string             `json:"aliasSymbol,omitempty"`
	UnionMembers        []*TypeJSON        `json:"unionMembers,omitempty"`
	IntersectionMembers []*TypeJSON        `json:"intersectionMembers,omitempty"`
	Properties          []TypePropertyJSON `json:"properties,omitempty"`
	CallSignatures      []SignatureJSON    `json:"callSignatures,omitempty"`
	ConstructSignatures []SignatureJSON    `json:"constructSignatures,omitempty"`
	TypeArguments       []*TypeJSON        `json:"typeArguments,omitempty"`
}

// TypePropertyJSON is one property of an object-like type.
type TypePropertyJSON struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Optional bool   `json:"optional,omitempty"`
}

// SignatureJSON is one call or construct signature.
type SignatureJSON struct {
	Params     []ParamJSON `json:"params"`
	ReturnType string      `json:"returnType"`
}

// ParamJSON is one signature parameter.
type ParamJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// EncodeType renders the structural JSON form of t. All type operations stay
// within the given checker; never pass a type obtained from a different
// checker instance. depth is the remaining expansion budget: at depth <= 0
// only display/flags/kind (and alias symbol) are emitted.
func EncodeType(c *checker.Checker, t *checker.Type, enclosing *ast.Node, depth int) *TypeJSON {
	if t == nil {
		return nil
	}
	out := &TypeJSON{
		Display: c.TypeToStringEx(t, enclosing, TypeDisplayFlags, nil),
		Flags:   checker.FormatTypeFlags(t.Flags()),
		Kind:    TypeKind(t),
	}
	if alias := t.Alias().Symbol(); alias != nil {
		out.AliasSymbol = alias.Name
	}
	if depth <= 0 {
		return out
	}
	display := func(t *checker.Type) string {
		return c.TypeToStringEx(t, enclosing, TypeDisplayFlags, nil)
	}
	switch out.Kind {
	case "union":
		for _, m := range t.Types() {
			out.UnionMembers = append(out.UnionMembers, EncodeType(c, m, enclosing, depth-1))
		}
	case "intersection":
		for _, m := range t.Types() {
			out.IntersectionMembers = append(out.IntersectionMembers, EncodeType(c, m, enclosing, depth-1))
		}
	case "object", "reference", "mapped":
		if t.ObjectFlags()&checker.ObjectFlagsReference != 0 {
			for _, arg := range c.GetTypeArguments(t) {
				out.TypeArguments = append(out.TypeArguments, EncodeType(c, arg, enclosing, depth-1))
			}
		}
		for _, p := range c.GetPropertiesOfType(t) {
			out.Properties = append(out.Properties, TypePropertyJSON{
				Name:     p.Name,
				Type:     display(c.GetTypeOfSymbol(p)),
				Optional: p.Flags&ast.SymbolFlagsOptional != 0,
			})
		}
		out.CallSignatures = encodeSignatures(c, t, checker.SignatureKindCall, display)
		out.ConstructSignatures = encodeSignatures(c, t, checker.SignatureKindConstruct, display)
	}
	return out
}

func encodeSignatures(c *checker.Checker, t *checker.Type, kind checker.SignatureKind, display func(*checker.Type) string) []SignatureJSON {
	var out []SignatureJSON
	for _, sig := range c.GetSignaturesOfType(t, kind) {
		encoded := SignatureJSON{
			Params:     make([]ParamJSON, 0, len(sig.Parameters())),
			ReturnType: display(c.GetReturnTypeOfSignature(sig)),
		}
		for _, p := range sig.Parameters() {
			encoded.Params = append(encoded.Params, ParamJSON{Name: p.Name, Type: display(c.GetTypeOfSymbol(p))})
		}
		out = append(out, encoded)
	}
	return out
}

// TypeKind classifies a type into the stable kind names used in tsagent
// output: primitive | literal | enum | union | intersection | object |
// reference | mapped | typeParameter | conditional | indexedAccess | index |
// templateLiteral | substitution.
func TypeKind(t *checker.Type) string {
	flags := t.Flags()
	switch {
	// `boolean` is internally a union of literals (TypeFlagsBoolean is not in
	// TypeFlagsIntrinsic) and enum-literal unions carry TypeFlagsUnion, so
	// intrinsic/literal checks come first.
	case flags&(checker.TypeFlagsIntrinsic|checker.TypeFlagsBoolean) != 0:
		return "primitive"
	case flags&(checker.TypeFlagsLiteral|checker.TypeFlagsUniqueESSymbol) != 0:
		return "literal"
	case flags&checker.TypeFlagsEnumLike != 0 && flags&checker.TypeFlagsUnion == 0:
		return "enum"
	case flags&checker.TypeFlagsUnion != 0:
		return "union"
	case flags&checker.TypeFlagsIntersection != 0:
		return "intersection"
	case flags&checker.TypeFlagsTypeParameter != 0:
		return "typeParameter"
	case flags&checker.TypeFlagsConditional != 0:
		return "conditional"
	case flags&checker.TypeFlagsIndexedAccess != 0:
		return "indexedAccess"
	case flags&checker.TypeFlagsIndex != 0:
		return "index"
	case flags&(checker.TypeFlagsTemplateLiteral|checker.TypeFlagsStringMapping) != 0:
		return "templateLiteral"
	case flags&checker.TypeFlagsSubstitution != 0:
		return "substitution"
	case flags&checker.TypeFlagsObject != 0:
		objectFlags := t.ObjectFlags()
		switch {
		case objectFlags&checker.ObjectFlagsMapped != 0:
			return "mapped"
		case objectFlags&checker.ObjectFlagsReference != 0:
			return "reference"
		default:
			return "object"
		}
	default:
		return "primitive"
	}
}

// ---------------------------------------------------------------------------
// Complexity metric

const complexityDepthCap = 20

// ComplexityBreakdown summarizes the main cost contributors of a type.
type ComplexityBreakdown struct {
	UnionWidth    int `json:"unionWidth"`
	PropertyCount int `json:"propertyCount"`
	Depth         int `json:"depth"`
}

// Complexity computes a structural cost metric for t: primitives and literals
// cost 1; unions/intersections cost 1 plus the sum of their members; object
// types cost 1 plus property count plus depth-limited property type costs
// plus signature costs; references cost 1 plus their type arguments;
// conditional types apply a x3 multiplier, mapped types x2, indexed accesses
// +2. Memoized on type ID with a depth cap of 20. All types must come from
// the given checker.
func Complexity(c *checker.Checker, t *checker.Type) int {
	cost, _ := ComplexityWithBreakdown(c, t)
	return cost
}

// ComplexityWithBreakdown is Complexity plus a contributor summary.
func ComplexityWithBreakdown(c *checker.Checker, t *checker.Type) (int, ComplexityBreakdown) {
	if t == nil {
		return 0, ComplexityBreakdown{}
	}
	scorer := &complexityScorer{c: c, memo: make(map[checker.TypeId]int)}
	cost := scorer.cost(t, 0)
	breakdown := ComplexityBreakdown{Depth: scorer.maxDepth}
	if t.IsUnion() || t.IsIntersection() {
		breakdown.UnionWidth = len(t.Types())
	}
	if t.Flags()&checker.TypeFlagsObject != 0 {
		breakdown.PropertyCount = len(c.GetPropertiesOfType(t))
	}
	return cost, breakdown
}

type complexityScorer struct {
	c        *checker.Checker
	memo     map[checker.TypeId]int
	maxDepth int
}

func (s *complexityScorer) cost(t *checker.Type, depth int) int {
	if t == nil {
		return 0
	}
	s.maxDepth = max(s.maxDepth, depth)
	if depth >= complexityDepthCap {
		return 1
	}
	if cached, ok := s.memo[t.Id()]; ok {
		return cached
	}
	s.memo[t.Id()] = 1 // cycle guard for recursive types
	cost := s.compute(t, depth)
	s.memo[t.Id()] = cost
	return cost
}

func (s *complexityScorer) compute(t *checker.Type, depth int) int {
	flags := t.Flags()
	switch {
	case flags&(checker.TypeFlagsIntrinsic|checker.TypeFlagsBoolean) != 0,
		flags&(checker.TypeFlagsLiteral|checker.TypeFlagsUniqueESSymbol|checker.TypeFlagsEnumLike) != 0 &&
			flags&checker.TypeFlagsUnion == 0:
		return 1
	case flags&checker.TypeFlagsTypeParameter != 0:
		return 1
	case flags&(checker.TypeFlagsUnion|checker.TypeFlagsIntersection) != 0:
		cost := 1
		for _, m := range t.Types() {
			cost += s.cost(m, depth+1)
		}
		return cost
	case flags&checker.TypeFlagsConditional != 0:
		conditional := t.AsConditionalType()
		return 3 * (1 + s.cost(conditional.CheckType(), depth+1) + s.cost(conditional.ExtendsType(), depth+1))
	case flags&checker.TypeFlagsIndexedAccess != 0:
		indexed := t.AsIndexedAccessType()
		return 2 + s.cost(indexed.ObjectType(), depth+1) + s.cost(indexed.IndexType(), depth+1)
	case flags&checker.TypeFlagsIndex != 0:
		return 1 + s.cost(t.AsIndexType().Target(), depth+1)
	case flags&(checker.TypeFlagsTemplateLiteral|checker.TypeFlagsStringMapping|checker.TypeFlagsSubstitution) != 0:
		return 2
	case flags&checker.TypeFlagsObject != 0:
		objectFlags := t.ObjectFlags()
		switch {
		case objectFlags&checker.ObjectFlagsMapped != 0:
			return 2 * (1 + s.structureCost(t, depth))
		case objectFlags&checker.ObjectFlagsReference != 0:
			cost := 1
			for _, arg := range s.c.GetTypeArguments(t) {
				cost += s.cost(arg, depth+1)
			}
			return cost
		default:
			return 1 + s.structureCost(t, depth)
		}
	default:
		return 1
	}
}

// structureCost is the property + signature cost of an object-like type.
func (s *complexityScorer) structureCost(t *checker.Type, depth int) int {
	properties := s.c.GetPropertiesOfType(t)
	cost := len(properties)
	for _, p := range properties {
		cost += s.cost(s.c.GetTypeOfSymbol(p), depth+1)
	}
	for _, kind := range []checker.SignatureKind{checker.SignatureKindCall, checker.SignatureKindConstruct} {
		for _, sig := range s.c.GetSignaturesOfType(t, kind) {
			cost += 1 + len(sig.Parameters()) + s.cost(s.c.GetReturnTypeOfSignature(sig), depth+1)
		}
	}
	return cost
}
