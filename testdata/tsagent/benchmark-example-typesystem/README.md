# benchmark-example-typesystem

Small dependency-free TypeScript fixture for iterating on `tsagent` memory and
type-system performance work. It is intentionally much smaller than a real app,
but it still exercises generic instantiation, conditional types, mapped types,
and template-literal union cross products.

The main hotspot is `src/hotspot.ts`. Useful symbols to look for in perf output
include `RouteKey`, `HandlerMap`, `RouteCatalog`, `DeepNormalize`, `RouteResponse`,
and `TypedSlot`.

No `npm install` is required.

From the repository root:

```sh
PROJECT=testdata/tsagent/benchmark-example-typesystem/tsconfig.json

go run ./cmd/tsagent map files --project "$PROJECT" --connect never
go run ./cmd/tsagent check --severity error --project "$PROJECT" --connect never

go run ./cmd/tsagent perf summary --single-threaded --project "$PROJECT" --connect never
go run ./cmd/tsagent perf hot-files --top 5 --single-threaded --project "$PROJECT" --connect never
go run ./cmd/tsagent perf hot-types --top 10 --single-threaded --project "$PROJECT" --connect never

go run ./cmd/tsagent report perf --top 10 --single-threaded --out /tmp/tsagent-typesystem-perf.html --project "$PROJECT" --connect never
```

For very quick smoke checks during tool work, start with `map files` and
`perf hot-types --top 5 --single-threaded`.

If you already built a local binary, replace `go run ./cmd/tsagent` with that
binary path.
