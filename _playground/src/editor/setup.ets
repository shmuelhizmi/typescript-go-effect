// Monaco setup: registers the EffectScript languages (Monarch grammar for
// instant highlighting) and wires the editor to the wasm LSP — pull
// diagnostics, hover, completions, and semantic tokens layered on top.
import * as monaco from "monaco-editor"
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker"
import type { LspClient } from "../compiler/lsp-client"
import { MAIN_URI } from "../compiler/protocol"
import { effectscriptConf, effectscriptLanguage } from "./monarch-effectscript"

const LANGUAGE_ID = "effectscript"
const MARKER_OWNER = "tsgo"

let environmentReady = false

export function ensureMonacoSetup(): void {
  if (environmentReady) return
  environmentReady = true

  self.MonacoEnvironment = {
    getWorker: () => new EditorWorker(),
  }

  for (const id of ["effectscript", "effectscriptreact"]) {
    monaco.languages.register({
      id,
      extensions: id === "effectscript" ? [".ets"] : [".etsx"],
      aliases: id === "effectscript" ? ["EffectScript"] : ["EffectScript React"],
    })
    monaco.languages.setLanguageConfiguration(id, effectscriptConf)
    monaco.languages.setMonarchTokensProvider(id, effectscriptLanguage)
  }
}

export interface PlaygroundEditor {
  editor: monaco.editor.IStandaloneCodeEditor
  model: monaco.editor.ITextModel
  setValue: (text: string) => void
}

export function createPlaygroundEditor(
  container: HTMLElement,
  initialSource: string,
  onChange: (text: string) => void,
): PlaygroundEditor {
  ensureMonacoSetup()
  const model = monaco.editor.createModel(initialSource, LANGUAGE_ID, monaco.Uri.parse(MAIN_URI))
  const editor = monaco.editor.create(container, {
    model,
    theme: "vs-dark",
    automaticLayout: true,
    minimap: { enabled: false },
    fontSize: 13,
    fixedOverflowWidgets: true,
    "semanticHighlighting.enabled": true,
  })
  model.onDidChangeContent(() => onChange(model.getValue()))
  return {
    editor,
    model,
    setValue: (text: string) => {
      if (model.getValue() !== text) model.setValue(text)
    },
  }
}

// --------------------------------------------------------------------------
// LSP wiring

interface LspRange {
  start: { line: number; character: number }
  end: { line: number; character: number }
}

interface LspDiagnostic {
  range: LspRange
  severity?: number
  code?: number | string
  message: string
}

interface SemanticTokensLegend {
  tokenTypes: string[]
  tokenModifiers: string[]
}

interface InitializeResult {
  capabilities: {
    completionProvider?: { triggerCharacters?: string[] }
    semanticTokensProvider?: { legend: SemanticTokensLegend }
  }
}

export interface LspConnection {
  /** Re-pulls diagnostics; called by the app whenever the source settles. */
  refreshDiagnostics: () => void
}

export async function connectLsp(
  lsp: LspClient,
  playground: PlaygroundEditor,
): Promise<LspConnection> {
  const { model } = playground

  const initResult = await lsp.request<InitializeResult>("initialize", {
    processId: null,
    rootUri: "file:///project",
    workspaceFolders: [{ uri: "file:///project", name: "project" }],
    capabilities: {
      textDocument: {
        publishDiagnostics: {},
        diagnostic: {},
        hover: { contentFormat: ["markdown", "plaintext"] },
        completion: { completionItem: { snippetSupport: false } },
        semanticTokens: {
          requests: { full: true },
          tokenTypes: [
            "namespace", "class", "enum", "interface", "typeParameter", "type",
            "parameter", "variable", "property", "enumMember", "function",
            "method", "member",
          ],
          tokenModifiers: ["declaration", "static", "async", "readonly", "defaultLibrary", "local"],
          formats: ["relative"],
        },
      },
    },
  })
  lsp.notify("initialized", {})
  lsp.notify("textDocument/didOpen", {
    textDocument: {
      uri: MAIN_URI,
      languageId: LANGUAGE_ID,
      version: 1,
      text: model.getValue(),
    },
  })

  let version = 1
  model.onDidChangeContent(() => {
    version++
    lsp.notify("textDocument/didChange", {
      textDocument: { uri: MAIN_URI, version },
      contentChanges: [{ text: model.getValue() }],
    })
  })

  registerHover(lsp)
  registerCompletions(lsp, initResult.capabilities.completionProvider?.triggerCharacters)
  const legend = initResult.capabilities.semanticTokensProvider?.legend
  if (legend) registerSemanticTokens(lsp, legend)

  const refreshDiagnostics = () => {
    void pullDiagnostics(lsp, model)
  }
  refreshDiagnostics()
  return { refreshDiagnostics }
}

async function pullDiagnostics(lsp: LspClient, model: monaco.editor.ITextModel): Promise<void> {
  try {
    const report = await lsp.request<{ kind: string; items: LspDiagnostic[] }>(
      "textDocument/diagnostic",
      { textDocument: { uri: MAIN_URI } },
    )
    if (report.kind !== "full") return
    monaco.editor.setModelMarkers(
      model,
      MARKER_OWNER,
      report.items.map(d => ({
        severity: toMarkerSeverity(d.severity),
        message: d.message,
        code: d.code === undefined ? undefined : String(d.code),
        startLineNumber: d.range.start.line + 1,
        startColumn: d.range.start.character + 1,
        endLineNumber: d.range.end.line + 1,
        endColumn: d.range.end.character + 1,
      })),
    )
  } catch {
    // The document changed underneath the request or the server is busy;
    // the next refresh will repaint.
  }
}

function toMarkerSeverity(severity: number | undefined): monaco.MarkerSeverity {
  switch (severity) {
    case 2:
      return monaco.MarkerSeverity.Warning
    case 3:
      return monaco.MarkerSeverity.Info
    case 4:
      return monaco.MarkerSeverity.Hint
    default:
      return monaco.MarkerSeverity.Error
  }
}

function toLspPosition(position: monaco.Position) {
  return { line: position.lineNumber - 1, character: position.column - 1 }
}

function toMonacoRange(range: LspRange): monaco.IRange {
  return {
    startLineNumber: range.start.line + 1,
    startColumn: range.start.character + 1,
    endLineNumber: range.end.line + 1,
    endColumn: range.end.character + 1,
  }
}

function registerHover(lsp: LspClient): void {
  monaco.languages.registerHoverProvider(LANGUAGE_ID, {
    provideHover: async (model, position) => {
      const result = await lsp.request<{
        contents: { kind: string; value: string } | string
        range?: LspRange
      } | null>("textDocument/hover", {
        textDocument: { uri: MAIN_URI },
        position: toLspPosition(position),
      })
      if (!result) return null
      const contents = typeof result.contents === "string" ? result.contents : result.contents.value
      const isMarkdown = typeof result.contents !== "string" && result.contents.kind === "markdown"
      return {
        contents: [{ value: isMarkdown ? contents : "```typescript\n" + contents + "\n```" }],
        range: result.range ? toMonacoRange(result.range) : undefined,
      }
    },
  })
}

interface LspCompletionItem {
  label: string
  kind?: number
  detail?: string
  sortText?: string
  filterText?: string
  insertText?: string
  textEdit?: { newText: string; range?: LspRange; insert?: LspRange; replace?: LspRange }
}

function registerCompletions(lsp: LspClient, triggerCharacters: string[] | undefined): void {
  monaco.languages.registerCompletionItemProvider(LANGUAGE_ID, {
    triggerCharacters: triggerCharacters ?? ["."],
    provideCompletionItems: async (model, position) => {
      const result = await lsp.request<
        { items: LspCompletionItem[] } | LspCompletionItem[] | null
      >("textDocument/completion", {
        textDocument: { uri: MAIN_URI },
        position: toLspPosition(position),
      })
      const items = Array.isArray(result) ? result : result?.items ?? []
      const word = model.getWordUntilPosition(position)
      const defaultRange: monaco.IRange = {
        startLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endLineNumber: position.lineNumber,
        endColumn: word.endColumn,
      }
      return {
        suggestions: items.map(item => {
          const editRange = item.textEdit?.range ?? item.textEdit?.replace ?? item.textEdit?.insert
          return {
            label: item.label,
            kind: toCompletionKind(item.kind),
            detail: item.detail,
            sortText: item.sortText,
            filterText: item.filterText,
            insertText: item.textEdit?.newText ?? item.insertText ?? item.label,
            range: editRange ? toMonacoRange(editRange) : defaultRange,
          }
        }),
      }
    },
  })
}

function toCompletionKind(kind: number | undefined): monaco.languages.CompletionItemKind {
  const kinds = monaco.languages.CompletionItemKind
  // LSP CompletionItemKind (1-25) -> Monaco
  const map: Record<number, monaco.languages.CompletionItemKind> = {
    1: kinds.Text, 2: kinds.Method, 3: kinds.Function, 4: kinds.Constructor,
    5: kinds.Field, 6: kinds.Variable, 7: kinds.Class, 8: kinds.Interface,
    9: kinds.Module, 10: kinds.Property, 11: kinds.Unit, 12: kinds.Value,
    13: kinds.Enum, 14: kinds.Keyword, 15: kinds.Snippet, 16: kinds.Color,
    17: kinds.File, 18: kinds.Reference, 19: kinds.Folder, 20: kinds.EnumMember,
    21: kinds.Constant, 22: kinds.Struct, 23: kinds.Event, 24: kinds.Operator,
    25: kinds.TypeParameter,
  }
  return (kind !== undefined && map[kind]) || kinds.Text
}

function registerSemanticTokens(lsp: LspClient, legend: SemanticTokensLegend): void {
  monaco.languages.registerDocumentSemanticTokensProvider(LANGUAGE_ID, {
    getLegend: () => legend,
    provideDocumentSemanticTokens: async () => {
      const result = await lsp.request<{ data: number[] } | null>(
        "textDocument/semanticTokens/full",
        { textDocument: { uri: MAIN_URI } },
      )
      if (!result) return null
      return { data: new Uint32Array(result.data), resultId: undefined }
    },
    releaseDocumentSemanticTokens: () => {},
  })
}
