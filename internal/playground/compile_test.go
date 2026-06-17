package playground_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/playground"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

func TestCompileEtsProject(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	fs := bundled.WrapFS(vfstest.FromMap(map[string]string{
		"/project/tsconfig.json": `{
			"compilerOptions": {
				"module": "esnext",
				"target": "es2022",
				"moduleResolution": "bundler",
				"strict": true,
				"skipLibCheck": true,
				"types": []
			},
			"files": ["main.ets"]
		}`,
		"/project/main.ets": `effect greet(name: string): string {
  return ` + "`hello, ${name}!`" + `
}

greet("world")
`,
	}, true))

	result := playground.Compile(t.Context(), fs, "/project", "/project/tsconfig.json", bundled.LibPath(), false)

	assert.Equal(t, len(result.Diagnostics), 0, "unexpected diagnostics: %v", result.Diagnostics)
	assert.Equal(t, len(result.Files), 1)
	assert.Equal(t, result.Files[0].Name, "/project/main.js")
	assert.Assert(t, strings.Contains(result.Files[0].Text, `from "effect/Effect"`), "emitted JS should import the effect/Effect subpath: %s", result.Files[0].Text)
}

func TestCompileReportsSyntacticDiagnostics(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	fs := bundled.WrapFS(vfstest.FromMap(map[string]string{
		"/project/tsconfig.json": `{"compilerOptions": {"module": "esnext"}, "files": ["main.ets"]}`,
		"/project/main.ets":      "effect broken(\n",
	}, true))

	result := playground.Compile(t.Context(), fs, "/project", "/project/tsconfig.json", bundled.LibPath(), false)

	assert.Assert(t, len(result.Diagnostics) > 0, "expected syntactic diagnostics")
	assert.Equal(t, result.Diagnostics[0].File, "/project/main.ets")
}
