# EffectScript example project

A minimal runnable `.ets` project: a `service`/`layer` pair, `effect`
declarations with `raises`/`requires` sugar, bind arrows (`<-`), `raise` +
`catch` arms, `par`, `defer`, `match`, and the `|>` pipeline — all running
against the real [`effect`](https://www.npmjs.com/package/effect) npm package.

For the full language reference see [`docs/effectscript/`](../../docs/effectscript/),
and for a program exercising every construct see
[`testdata/tests/cases/effectscript/integration/quickstart.ets`](../../testdata/tests/cases/effectscript/integration/quickstart.ets).

## Run it

```sh
npm install
npm run build   # go run ../../cmd/tsgo -p .
npm start       # node dist/main.js
```

Expected output (modulo timestamp):

```
timestamp=... level=INFO fiber=#0 message=done
Hello, user-42! | Hello, world! | seven
```

`npm run build` compiles the fork from source via `go run`; if you have a
prebuilt binary, `path/to/tsgo -p .` does the same thing.

## Editor setup

The LSP server is `tsgo --lsp --stdio`; it picks up `.ets`/`.etsx` from the
file extension, so any editor works by treating these files as
TypeScript/TSX and pointing the TypeScript language server at the fork.

**VS Code**: use the extension in [`_extension/`](../../_extension/), which
registers the `effectscript`/`effectscriptreact` languages.

**Zed**: build a binary (`go build -o ~/.local/share/tsgo-effect/tsgo ./cmd/tsgo`
from the repo root), then in `settings.json`:

```jsonc
{
  "file_types": {
    "TypeScript": ["ets"],
    "TSX": ["etsx"]
  },
  "languages": {
    "TypeScript": { "semantic_tokens": "combined" },
    "TSX": { "semantic_tokens": "combined" }
  },
  "lsp": {
    "vtsls": {
      "binary": {
        "path": "/path/to/tsgo",
        "arguments": ["--lsp", "--stdio"]
      }
    }
  }
}
```
