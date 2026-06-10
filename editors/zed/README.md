# EffectScript in Zed

This is a [Zed](https://zed.dev) extension that gives `.ets` / `.etsx` files
diagnostics, hover, go-to-definition, and completions by running the
`typescript-go-effect` language server (`tsgo --lsp --stdio`).

Zed can't register a brand-new language server from `settings.json` alone, so a
small dev extension is required. It's self-contained in
`editors/zed/effectscript/`.

## 1. Build the language server

From the repo root:

```sh
npx hereby build          # produces ./built/local/tsgo
```

(or `go build -o built/local/tsgo ./cmd/tsgo`).

The extension finds the binary automatically in this order:

1. `lsp.effectscript-lsp.binary.path` in your Zed settings, if set;
2. a `tsgo` on your `PATH`;
3. `./built/local/tsgo` relative to the worktree root (the default above).

If you build elsewhere, or want to use it outside this repo, put `tsgo` on your
`PATH` or pin it explicitly (see step 3).

## 2. Install the extension as a dev extension

In Zed: open the command palette → **zed: install dev extension** → select the
folder `editors/zed/effectscript`.

Zed compiles the extension to WebAssembly (you need the Rust toolchain with the
`wasm32-wasip2` target: `rustup target add wasm32-wasip2`). On success, `.ets`
and `.etsx` files are recognized as **EffectScript** / **EffectScript React**
and the server starts when you open one.

## 3. (Optional) pin the server path

In your Zed `settings.json`:

```json
{
  "lsp": {
    "effectscript-lsp": {
      "binary": {
        "path": "/absolute/path/to/typescript-go-effect/built/local/tsgo",
        "arguments": ["--lsp", "--stdio"]
      }
    }
  }
}
```

## Notes & limitations

- **Syntax highlighting** reuses the tree-sitter TypeScript/TSX grammars, which
  don't know EffectScript's contextual keywords (`effect`, `raise`, `<-`,
  `match`, `service`, `layer`, …). They render as identifiers/operators; the
  *semantics* (errors, types, navigation) come from the language server, which
  fully understands the syntax.
- The server type-checks the **lowered** program, so a diagnostic about an
  effect's `A`/`E`/`R` channels reads in terms of `Effect.Effect<…>` — exactly
  what the desugared code says. See `docs/effectscript/` for the language.
- `tsgo` shells out to `npm` to install missing `@types` packages; make sure
  `node`/`npm` are on your `PATH`.

## Troubleshooting

- *"could not find the `tsgo` language server"* — build it (step 1) or set the
  path (step 3).
- *No diagnostics* — confirm the file ends in `.ets`/`.etsx` and there's a
  `tsconfig.json` in the project; check Zed's language-server logs
  (command palette → **dev: open language server logs**).
- *Wrong `zed_extension_api` version* — if the build fails on the API version,
  bump `zed_extension_api` in `effectscript/Cargo.toml` to the version matching
  your Zed (see <https://github.com/zed-industries/zed/releases>).
