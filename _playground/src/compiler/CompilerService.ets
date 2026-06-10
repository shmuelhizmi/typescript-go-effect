// The Compiler service: EffectScript-native wrapper around the wasm worker.
import { Data, Effect, Layer } from "effect"
import type { CompileResult } from "./protocol"
import { getWorkerClient } from "./worker-client"

export class CompileError extends Data.TaggedError("CompileError")<{ message: string }> {}

export service Compiler {
  compile: (source: string) => Effect.Effect<CompileResult, CompileError>
}

export const makeCompilerLayer = (initialSource: string) =>
  Layer.succeed(Compiler, {
    compile: (source: string) =>
      Effect.tryPromise({
        try: () => getWorkerClient(initialSource).compilePromise(source),
        catch: (cause: unknown) =>
          new CompileError({ message: cause instanceof Error ? cause.message : String(cause) }),
      }),
  })
