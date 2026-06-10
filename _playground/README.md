# EffectScript Playground

A fully client-side, TypeScript-Playground-style web app for EffectScript:
the fork's compiler runs **in the browser** as WebAssembly, providing live
diagnostics, hover, completions, and semantic tokens through its real LSP,
plus transpile-and-run with the full `effect` library loaded.

The app **dogfoods the language**: its own source is written in `.ets`/`.etsx`
(see `src/`), compiled by the native tsgo via `vite/vite-plugin-ets.ts`, with
state managed by [effect-atom](https://github.com/tim-smart/effect-atom).

## Run it

```sh
npm install
npm run dev   # builds native tsgo + tsgo.wasm (~50MB) + asset packs, starts Vite
```

Then open http://localhost:5173.

Other commands:

- `npm run check` — typecheck the app with the fork itself
- `npm test` — vitest unit tests (LSP framing)
- `npm run smoke` — drive tsgo.wasm under Node (LSP handshake, diagnostics,
  hover, completion, semantic tokens, compile) with timings
- `npm run build` / `npm run preview` — production build

## Architecture

```
Main thread                          Web Worker
┌────────────────────────────┐      ┌─────────────────────────────┐
│ React app (.etsx, effect-  │      │ tsgo.wasm (Go)              │
│ atom state)                │      │  - LSP server (in-memory FS)│
│ Monaco editor              │ LSP  │  - compile() -> JS          │
│  - Monarch grammar (.ets)  │◀────▶│ framing shim bytes⇄JSON     │
│  - thin LSP client:        │ port │ types-pack.bin.gz loaded at │
│    diagnostics/hover/      │      │ /project/node_modules/...   │
│    completion/sem. tokens  │      └─────────────────────────────┘
└──────────┬─────────────────┘
           │ Run: sandboxed iframe, import map -> public/vendor/effect
           ▼   (pre-bundled effect ESM), console piped via postMessage
```

- `cmd/tsgo-wasm` (repo root) exposes `globalThis.tsgoWasm`; built by
  `npx hereby tsgo:wasm`.
- `scripts/build-types-pack.mjs` packs effect's `.d.ts` surface (~0.6MB gz)
  for the in-wasm workspace; `scripts/build-effect-vendor.mjs` bundles the
  effect runtime (every subpath export) for the run sandbox's import map.
- `src/worker/compiler.worker.ts` boots the wasm and bridges
  Content-Length-framed LSP bytes to parsed JSON messages on a MessagePort.
- `vite/vite-plugin-ets.ts` compiles the whole app project with the native
  tsgo (`--noCheck`, ~100ms) into `node_modules/.ets-cache` and serves the
  emitted JS; `.ets`/`.etsx` saves trigger recompile + full reload.

Known limitation: a synchronous infinite loop in playground code blocks its
iframe; re-running replaces the iframe (fresh realm), which stops timers and
fibers but cannot kill a hot sync loop.
