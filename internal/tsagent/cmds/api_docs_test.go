package cmds

import (
	"context"
	"strings"
	"testing"
)

func apiDocsTestFiles() map[string]any {
	return map[string]any{
		"/project/src/index.ts": `/**
 * Greets a user by name.
 *
 * Longer remarks that belong to the second paragraph.
 * @param name - the user to greet
 * @returns the greeting line
 * @deprecated use greetAll instead
 */
export function greet(name: string): string {
	return "hi " + name;
}

/** Maximum retry count. */
export const MAX_RETRIES: number = 3;

export const undocumented = true;
`,
	}
}

func findApiDoc(t *testing.T, result *ApiDocsResult, name string) *ApiDoc {
	t.Helper()
	for _, d := range result.Docs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no doc for %s (have %d docs)", name, len(result.Docs))
	return nil
}

func TestApiDocsExtractsJSDoc(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, apiDocsTestFiles())
	result, err := runApiDocs(context.Background(), ws, &apiDocsFlags{}, nil)
	if err != nil {
		t.Fatalf("runApiDocs: %v", err)
	}
	if result.Total() != 3 {
		t.Fatalf("expected 3 docs, got %d", result.Total())
	}

	greet := findApiDoc(t, result, "greet")
	if greet.Kind != "function" || !strings.Contains(greet.Signature, "(name: string): string") {
		t.Errorf("greet kind/signature = %q %q", greet.Kind, greet.Signature)
	}
	if greet.SymbolID != "src/index.ts#greet" {
		t.Errorf("greet symbolId = %q", greet.SymbolID)
	}
	if !strings.HasPrefix(greet.DeclaredAt, "src/index.ts:") {
		t.Errorf("greet declaredAt = %q", greet.DeclaredAt)
	}
	if greet.JSDoc == nil {
		t.Fatal("greet has no jsdoc")
	}
	if greet.JSDoc.Summary != "Greets a user by name." {
		t.Errorf("summary = %q, want first paragraph only", greet.JSDoc.Summary)
	}
	tags := map[string]ApiDocTag{}
	for _, tag := range greet.JSDoc.Tags {
		tags[tag.Tag] = tag
	}
	if param, ok := tags["param"]; !ok || param.Name != "name" || !strings.Contains(param.Text, "the user to greet") {
		t.Errorf("@param = %+v", tags["param"])
	}
	if returns, ok := tags["returns"]; !ok || !strings.Contains(returns.Text, "greeting line") {
		t.Errorf("@returns = %+v", tags["returns"])
	}
	if deprecated, ok := tags["deprecated"]; !ok || !strings.Contains(deprecated.Text, "greetAll") {
		t.Errorf("@deprecated = %+v", tags["deprecated"])
	}

	max := findApiDoc(t, result, "MAX_RETRIES")
	if max.Kind != "const" || max.JSDoc == nil || max.JSDoc.Summary != "Maximum retry count." {
		t.Errorf("MAX_RETRIES = %+v (jsdoc %+v)", max, max.JSDoc)
	}

	plain := findApiDoc(t, result, "undocumented")
	if plain.JSDoc != nil {
		t.Errorf("undocumented export should have no jsdoc, got %+v", plain.JSDoc)
	}
}

func TestApiDocsByNameAndSymbol(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, apiDocsTestFiles())
	ctx := context.Background()

	result, err := runApiDocs(ctx, ws, &apiDocsFlags{name: "greet"}, nil)
	if err != nil {
		t.Fatalf("runApiDocs --name: %v", err)
	}
	if result.Total() != 1 || result.Docs[0].Name != "greet" || result.Docs[0].JSDoc == nil {
		t.Errorf("--name greet = %+v", result.Docs)
	}

	result, err = runApiDocs(ctx, ws, &apiDocsFlags{}, []string{"src/index.ts#MAX_RETRIES"})
	if err != nil {
		t.Fatalf("runApiDocs symbol arg: %v", err)
	}
	if result.Total() != 1 || result.Docs[0].Name != "MAX_RETRIES" {
		t.Errorf("symbol target = %+v", result.Docs)
	}
}
