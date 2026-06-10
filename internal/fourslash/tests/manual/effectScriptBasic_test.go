package fourslash_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/fourslash"
	"github.com/microsoft/typescript-go/internal/testutil"
)

// Smoke test: the language server opens .ets files, the EffectScript lowering
// runs, and hover works on a bind target whose type flows from the lowered
// yield* expression.
func TestEffectScriptBasicLanguageService(t *testing.T) {
	t.Parallel()

	defer testutil.RecoverAndFail(t, "Panic on fourslash test")
	const content = `// @target: esnext
// @Filename: main.ets
declare const numberEffect: any;
effect compute/*0*/() {
  value/*1*/: number <- numberEffect
  return value/*2*/ + 1
}`
	f, done := fourslash.NewFourslash(t, nil /*capabilities*/, content)
	defer done()
	f.GoToMarker(t, "0")
	f.VerifyQuickInfoExists(t)
	f.VerifyQuickInfoAt(t, "1", "const value: number", "")
	f.VerifyQuickInfoAt(t, "2", "const value: number", "")
}
