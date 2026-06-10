package lsp_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/lsp"
	"github.com/microsoft/typescript-go/internal/lsp/lsproto"
	"github.com/microsoft/typescript-go/internal/testutil/lsptestutil"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

// Hover in EffectScript files: lowered-away keywords (`effect`, `layer`,
// `raise`, ...) must produce no hover instead of a bogus `any`, while
// user-written bindings inside lowered constructs (a catch-arm `as e`, a
// service member) must resolve to their real symbols.
func TestHoverEffectScript(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	source := `service Greeter {
  greeting: string
}

effect findUser(id: string): string raises Error {
  return id
}

effect main(): string {
  user <- findUser("42") catch {
    _ as e >> "fallback"
  }
  return user
}
`

	files := map[string]string{
		"/home/projects/tsconfig.json": `{"compilerOptions": {"strict": true}, "files": ["main.ets"]}`,
		"/home/projects/main.ets":      source,
	}
	fs := bundled.WrapFS(vfstest.FromMap(files, false))

	onServerRequest := func(_ context.Context, req *lsproto.RequestMessage) *lsproto.ResponseMessage {
		if req.Method == lsproto.MethodClientRegisterCapability || req.Method == lsproto.MethodClientUnregisterCapability {
			return &lsproto.ResponseMessage{ID: req.ID, JSONRPC: req.JSONRPC, Result: lsproto.Null{}}
		}
		return nil
	}

	client, closeClient := lsptestutil.NewLSPClient(t, lsp.ServerOptions{
		Err: io.Discard, Cwd: "/home/projects", FS: fs, DefaultLibraryPath: bundled.LibPath(),
	}, onServerRequest)
	t.Cleanup(func() { _ = closeClient() })

	initMsg, _, ok := lsptestutil.SendRequest(t, client, lsproto.InitializeInfo, &lsproto.InitializeParams{
		Capabilities: &lsproto.ClientCapabilities{},
	})
	assert.Assert(t, ok && initMsg.AsResponse().Error == nil, "Initialize failed")
	lsptestutil.SendNotification(t, client, lsproto.InitializedInfo, &lsproto.InitializedParams{})
	<-client.Server.InitComplete()

	uri := lsproto.DocumentUri("file:///home/projects/main.ets")
	lsptestutil.SendNotification(t, client, lsproto.TextDocumentDidOpenInfo, &lsproto.DidOpenTextDocumentParams{
		TextDocument: &lsproto.TextDocumentItem{Uri: uri, LanguageId: "effectscript", Text: source},
	})

	hoverAt := func(needle string, word string) *lsproto.Hover {
		t.Helper()
		idx := strings.Index(source, needle)
		assert.Assert(t, idx >= 0, "needle %q not found", needle)
		offset := idx + strings.Index(source[idx:], word) + 1
		prefix := source[:offset]
		line := strings.Count(prefix, "\n")
		character := offset - (strings.LastIndex(prefix, "\n") + 1)
		msg, result, ok := lsptestutil.SendRequest(t, client, lsproto.TextDocumentHoverInfo, &lsproto.HoverParams{
			TextDocument: lsproto.TextDocumentIdentifier{Uri: uri},
			Position:     lsproto.Position{Line: uint32(line), Character: uint32(character)},
		})
		assert.Assert(t, ok, "hover request failed")
		assert.Assert(t, msg.AsResponse().Error == nil, "hover error: %v", msg.AsResponse().Error)
		return result.Hover
	}

	hoverText := func(hover *lsproto.Hover) string {
		if hover == nil || hover.Contents.MarkupContent == nil {
			return ""
		}
		return hover.Contents.MarkupContent.Value
	}

	// Lowered-away keywords: no hover at all (TS keywords behave the same).
	for _, probe := range []struct{ needle, word string }{
		{"service Greeter", "service"},
		{"effect findUser", "effect"},
		{"raises Error", "raises"},
	} {
		if hover := hoverAt(probe.needle, probe.word); hover != nil {
			t.Errorf("hover on %q: expected none, got %q", probe.word, hoverText(hover))
		}
	}

	// User-written symbols inside lowered constructs resolve for real.
	for _, probe := range []struct{ needle, word, expect string }{
		{"_ as e >>", "e", "(parameter) e"},
		{"greeting: string", "greeting", "(property) greeting"},
		{"user <- findUser", "user", "const user"},
	} {
		text := hoverText(hoverAt(probe.needle, probe.word))
		if !strings.Contains(text, probe.expect) {
			t.Errorf("hover on %q: expected to contain %q, got %q", probe.word, probe.expect, text)
		}
	}
}
