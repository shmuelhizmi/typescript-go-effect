package core

import "github.com/microsoft/typescript-go/internal/ast"

// DeclarationKind classifies a declaration node into the stable kind names
// used across tsagent output and --kind filters.
func DeclarationKind(node *ast.Node) string {
	switch node.Kind {
	case ast.KindFunctionDeclaration:
		return "function"
	case ast.KindClassDeclaration, ast.KindClassExpression:
		return "class"
	case ast.KindInterfaceDeclaration:
		return "interface"
	case ast.KindEnumDeclaration:
		return "enum"
	case ast.KindEnumMember:
		return "enummember"
	case ast.KindTypeAliasDeclaration:
		return "type"
	case ast.KindModuleDeclaration:
		return "namespace"
	case ast.KindMethodDeclaration, ast.KindMethodSignature:
		return "method"
	case ast.KindPropertyDeclaration, ast.KindPropertySignature, ast.KindPropertyAssignment, ast.KindShorthandPropertyAssignment:
		return "property"
	case ast.KindConstructor:
		return "constructor"
	case ast.KindGetAccessor:
		return "getter"
	case ast.KindSetAccessor:
		return "setter"
	case ast.KindVariableDeclaration, ast.KindBindingElement:
		if ast.GetCombinedNodeFlags(node)&ast.NodeFlagsConst != 0 {
			return "const"
		}
		if ast.GetCombinedNodeFlags(node)&ast.NodeFlagsLet != 0 {
			return "let"
		}
		return "var"
	case ast.KindParameter:
		return "parameter"
	case ast.KindTypeParameter:
		return "typeparameter"
	case ast.KindFunctionExpression, ast.KindArrowFunction:
		return "function"
	case ast.KindImportSpecifier, ast.KindImportClause, ast.KindNamespaceImport, ast.KindImportEqualsDeclaration:
		return "import"
	case ast.KindIndexSignature:
		return "indexsignature"
	case ast.KindCallSignature:
		return "callsignature"
	case ast.KindConstructSignature:
		return "constructsignature"
	}
	return "unknown"
}

// KindMatchesFilter reports whether a kind satisfies a --kind filter value,
// treating variable-ish kinds as interchangeable with "variable".
func KindMatchesFilter(kind string, filter map[string]bool) bool {
	if len(filter) == 0 {
		return true
	}
	if filter[kind] {
		return true
	}
	switch kind {
	case "const", "let", "var":
		return filter["variable"]
	case "namespace":
		return filter["module"]
	}
	return false
}

// IsExportedDeclaration reports whether a declaration carries an export
// modifier (including `export default`).
func IsExportedDeclaration(node *ast.Node) bool {
	return ast.GetCombinedModifierFlags(node)&ast.ModifierFlagsExport != 0
}
