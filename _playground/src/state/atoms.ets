// The playground's state model, managed with effect-atom: source edits flow
// through a debounce into the wasm compiler, producing a Result the panes
// render. The compile pipeline itself is an EffectScript `effect`.
import { Atom } from "@effect-atom/atom-react"
import { CompileError, Compiler, makeCompilerLayer } from "../compiler/CompilerService"
import type { CompileResult } from "../compiler/protocol"
import { initialSource } from "../examples"

export type TabId = "js" | "run" | "errors"

export const sourceAtom = Atom.make(initialSource).pipe(Atom.keepAlive)

const debouncedSourceAtom = Atom.debounce(sourceAtom, "400 millis")

const runtimeAtom = Atom.runtime(makeCompilerLayer(initialSource))

effect compileSource(source: string): CompileResult raises CompileError requires Compiler {
  compiler <- Compiler
  result <- compiler.compile(source)
  return result
}

export const compileAtom = runtimeAtom
  .atom((get: Atom.Context) => compileSource(get(debouncedSourceAtom)))
  .pipe(Atom.keepAlive)

export const activeTabAtom = Atom.make<TabId>("js")

/** Bumped by the Run button; the run pane executes on change. */
export const runNonceAtom = Atom.make(0)
