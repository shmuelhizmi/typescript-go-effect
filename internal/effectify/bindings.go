package effectify

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// helperNames are the namespaces (plus pipe) the desugarer references; only
// files binding them under their canonical names are rewritten, so that the
// reverse mapping (Effect.gen → effect { }) matches the forward one exactly.
var helperNames = map[string]bool{
	"Effect": true, "Layer": true, "Context": true, "Fiber": true, "Match": true, "pipe": true,
}

type effectBindings struct {
	// helpers holds the canonical helper names imported (unaliased) from the
	// effect import source.
	helpers map[string]bool
	// specifiers are the import specifier nodes that bind those helpers;
	// exempt from the shadowing scan.
	specifiers map[*ast.Node]bool
	// aliased is true when a helper is imported under a different local name
	// (import { Effect as E }) — the reverse mapping would not round-trip.
	aliased bool
	// namespace is true for `import * as Eff from "effect"`; depth-2 access
	// (Eff.Effect.gen) is not recognized in v1.
	namespace bool
}

func (b effectBindings) skipReason() string {
	switch {
	case b.aliased:
		return SkipAliasedImport
	case b.namespace && len(b.helpers) == 0:
		return SkipNamespaceImport
	case len(b.helpers) == 0:
		return SkipNoEffectImport
	}
	return ""
}

// scanBindings inspects the file's top-level imports of importSource and
// records which helper namespaces are bound.
func scanBindings(file *ast.SourceFile, importSource string) effectBindings {
	b := effectBindings{helpers: make(map[string]bool), specifiers: make(map[*ast.Node]bool)}
	for _, s := range file.Statements.Nodes {
		if s.Kind != ast.KindImportDeclaration {
			continue
		}
		imp := s.AsImportDeclaration()
		if imp.ModuleSpecifier == nil || imp.ModuleSpecifier.Kind != ast.KindStringLiteral || imp.ModuleSpecifier.Text() != importSource {
			continue
		}
		clause := imp.ImportClause
		if clause == nil || clause.IsTypeOnly() {
			continue
		}
		named := clause.AsImportClause().NamedBindings
		if named == nil {
			continue
		}
		switch named.Kind {
		case ast.KindNamespaceImport:
			b.namespace = true
		case ast.KindNamedImports:
			for _, el := range named.AsNamedImports().Elements.Nodes {
				spec := el.AsImportSpecifier()
				if spec.IsTypeOnly {
					continue
				}
				local := el.Name().Text()
				imported := local
				if spec.PropertyName != nil {
					imported = spec.PropertyName.Text()
				}
				if !helperNames[imported] {
					continue
				}
				if imported != local {
					b.aliased = true
					continue
				}
				b.helpers[local] = true
				b.specifiers[el] = true
			}
		}
	}
	return b
}

// findHelperShadowing walks the whole file looking for any declaration that
// rebinds a tracked helper name (a nested `const Effect = …`, a parameter
// named Match, an `Effect` imported from some other module, …). Returns the
// offending name, or "" when clean. The import specifiers that *are* the
// tracked helper bindings are exempt.
func findHelperShadowing(file *ast.SourceFile, b effectBindings) string {
	found := ""
	var visit func(node *ast.Node) bool
	visit = func(node *ast.Node) bool {
		if found != "" {
			return true
		}
		switch node.Kind {
		case ast.KindImportSpecifier:
			if !b.specifiers[node] {
				if name := node.Name(); name != nil && b.helpers[name.Text()] {
					found = name.Text()
					return true
				}
			}
		case ast.KindImportClause, ast.KindNamespaceImport, ast.KindImportEqualsDeclaration,
			ast.KindVariableDeclaration, ast.KindParameter, ast.KindBindingElement,
			ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindClassExpression,
			ast.KindFunctionExpression, ast.KindEnumDeclaration, ast.KindModuleDeclaration:
			if name := node.Name(); name != nil && name.Kind == ast.KindIdentifier && b.helpers[name.Text()] {
				found = name.Text()
				return true
			}
		}
		return node.ForEachChild(visit)
	}
	file.AsNode().ForEachChild(visit)
	return found
}
