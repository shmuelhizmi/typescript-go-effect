package checker_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

// EffectScript lowers constructs at parse time; this verifies the lowered
// tree supports position-based symbol resolution (the LSP hover path) at both
// the declaration and use site of a bind.
func TestEffectScriptSymbolAtLocation(t *testing.T) {
	t.Parallel()
	content := `declare const numberEffect: any;
effect compute() {
  value: number <- numberEffect
  return value + 1
}`
	fs := vfstest.FromMap(map[string]string{
		"/main.ets":      content,
		"/tsconfig.json": `{"compilerOptions": {"target": "esnext"}, "files": ["main.ets"]}`,
	}, false)
	fs = bundled.WrapFS(fs)
	host := compiler.NewCompilerHost("/", fs, bundled.LibPath(), nil, nil)
	parsed, errs := tsoptions.GetParsedCommandLineOfConfigFile("/tsconfig.json", &core.CompilerOptions{}, nil, host, nil)
	assert.Equal(t, len(errs), 0)
	p := compiler.NewProgram(compiler.ProgramOptions{Config: parsed, Host: host})
	p.BindSourceFiles()
	c, done := p.GetTypeChecker(t.Context())
	defer done()
	file := p.GetSourceFile("/main.ets")

	declPos := strings.Index(content, "value") + 3
	usePos := strings.LastIndex(content, "value") + 3
	for _, pos := range []int{declPos, usePos} {
		tok := astnav.GetTouchingPropertyName(file, pos)
		sym := c.GetSymbolAtLocation(tok)
		assert.Assert(t, sym != nil, "symbol at %d", pos)
		assert.Equal(t, sym.Name, "value")
		assert.Equal(t, c.TypeToString(c.GetTypeOfSymbolAtLocation(sym, tok)), "number")
	}
}
