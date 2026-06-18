package effectify

import (
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
)

// namespaceHelpers are the helper *namespaces* the desugarer references —
// imported either from the barrel (`import { Effect } from "effect"`) or from a
// subpath (`import * as Effect from "effect/Effect"`), and accessed as
// `Effect.method(…)`.
var namespaceHelpers = map[string]bool{
	"Effect": true, "Layer": true, "Context": true, "Fiber": true, "Match": true, "Schema": true, "Data": true,
}

// helperNames are everything that can be bound from the barrel under its own
// name: the namespaces plus the free `pipe` function.
var helperNames = map[string]bool{
	"Effect": true, "Layer": true, "Context": true, "Fiber": true, "Match": true, "Schema": true, "Data": true, "pipe": true,
}

type effectBindings struct {
	// localToCanonical maps each bound local name (Effect, E, …) to the
	// canonical helper namespace it refers to (Effect, Layer, …, or pipe).
	// Covers barrel named imports, aliased named imports, and subpath
	// namespace imports (import * as Effect from "effect/Effect").
	localToCanonical map[string]string
	// barrelRoots are local names bound to `import * as X from "effect"`,
	// accessed at depth 2 (X.Effect.gen).
	barrelRoots map[string]bool
	// bound is the set of canonical helpers reachable in this file (under any
	// local name); consulted by the pipe/type-sugar gates.
	bound map[string]bool
	// specifiers are the import nodes that bind those helpers; exempt from the
	// shadowing scan.
	specifiers map[*ast.Node]bool
}

func (b effectBindings) skipReason() string {
	if len(b.localToCanonical) == 0 && len(b.barrelRoots) == 0 {
		return SkipNoEffectImport
	}
	return ""
}

// isTrackedLocal reports whether name is a local binding the rewriter relies on
// (a helper alias or a barrel root); used by the shadowing scan.
func (b effectBindings) isTrackedLocal(name string) bool {
	if _, ok := b.localToCanonical[name]; ok {
		return true
	}
	return b.barrelRoots[name]
}

// scanBindings inspects the file's top-level imports of importSource (and its
// `importSource/<Namespace>` subpaths) and records which helper namespaces are
// bound, under which local names.
func scanBindings(file *ast.SourceFile, importSource string) effectBindings {
	b := effectBindings{
		localToCanonical: make(map[string]string),
		barrelRoots:      make(map[string]bool),
		bound:            make(map[string]bool),
		specifiers:       make(map[*ast.Node]bool),
	}
	subpathPrefix := importSource + "/"
	for _, s := range file.Statements.Nodes {
		if s.Kind != ast.KindImportDeclaration {
			continue
		}
		imp := s.AsImportDeclaration()
		if imp.ModuleSpecifier == nil || imp.ModuleSpecifier.Kind != ast.KindStringLiteral {
			continue
		}
		src := imp.ModuleSpecifier.Text()
		isBarrel := src == importSource
		subpathCanon := ""
		if !isBarrel {
			if !strings.HasPrefix(src, subpathPrefix) {
				continue
			}
			sub := src[len(subpathPrefix):]
			if !namespaceHelpers[sub] {
				continue // a subpath we don't model (effect/Schedule, …)
			}
			subpathCanon = sub
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
			local := named.Name().Text()
			b.specifiers[named] = true
			if isBarrel {
				b.barrelRoots[local] = true
			} else {
				// import * as <local> from "effect/<Canonical>"
				b.localToCanonical[local] = subpathCanon
				b.bound[subpathCanon] = true
			}
		case ast.KindNamedImports:
			if !isBarrel {
				// Bare-named subpath imports (import { gen } from
				// "effect/Effect") turn every helper method into a free
				// identifier — out of scope; left verbatim.
				continue
			}
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
				b.localToCanonical[local] = imported
				b.bound[imported] = true
				b.specifiers[el] = true
			}
		}
	}
	return b
}

// findHelperShadowing walks the whole file looking for any declaration that
// rebinds a tracked local name (a nested `const Effect = …`, a parameter named
// Match, an `Effect` imported from some other module, …). Returns the offending
// name, or "" when clean. The import specifiers that *are* the tracked helper
// bindings are exempt.
func findHelperShadowing(file *ast.SourceFile, b effectBindings) string {
	found := ""
	var visit func(node *ast.Node) bool
	visit = func(node *ast.Node) bool {
		if found != "" {
			return true
		}
		switch node.Kind {
		case ast.KindImportSpecifier, ast.KindNamespaceImport:
			if !b.specifiers[node] {
				if name := node.Name(); name != nil && b.isTrackedLocal(name.Text()) {
					found = name.Text()
					return true
				}
			}
		case ast.KindImportClause, ast.KindImportEqualsDeclaration,
			ast.KindVariableDeclaration, ast.KindParameter, ast.KindBindingElement,
			ast.KindFunctionDeclaration, ast.KindClassDeclaration, ast.KindClassExpression,
			ast.KindFunctionExpression, ast.KindEnumDeclaration, ast.KindModuleDeclaration:
			if name := node.Name(); name != nil && name.Kind == ast.KindIdentifier && b.isTrackedLocal(name.Text()) {
				found = name.Text()
				return true
			}
		}
		return node.ForEachChild(visit)
	}
	file.AsNode().ForEachChild(visit)
	return found
}
