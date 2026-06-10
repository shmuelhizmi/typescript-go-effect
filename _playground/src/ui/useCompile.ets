// React view over the compile Result atom.
import { Result, useAtomValue } from "@effect-atom/atom-react"
import { Cause } from "effect"
import type { CompileResult } from "../compiler/protocol"
import { compileAtom } from "../state/atoms"

export type CompileView =
  | { kind: "initial" }
  | { kind: "error"; message: string }
  | { kind: "ok"; result: CompileResult }

export function useAtomCompileResult(): CompileView {
  const result = useAtomValue(compileAtom)
  if (Result.isSuccess(result)) return { kind: "ok", result: result.value }
  if (Result.isFailure(result)) return { kind: "error", message: Cause.pretty(result.cause) }
  return { kind: "initial" }
}
