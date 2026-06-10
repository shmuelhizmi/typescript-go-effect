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
    signatureHelpProvider?: { triggerCharacters?: string[]; retriggerCharacters?: string[] }
    semanticTokensProvider?: { legend: SemanticTokensLegend }
  }
}

export interface LspConnection {
  /** Re-pulls diagnostics; called by the app whenever the source settles. */
  refreshDiagnostics: () => void
}

/** monaco model URI <-> the paths/URIs the wasm LSP understands. */
function lspUri(model: monaco.editor.ITextModel): string {
  return model.uri.toString()
}

function uriToFsPath(uri: monaco.Uri): string {
  // file:///project/x -> /project/x; bundled:///libs/x stays whole (the Go
  // FS dispatches on the scheme prefix).
  return uri.scheme === "file" ? uri.path : uri.toString()
}

export async function connectLsp(
  lsp: LspClient,
  playground: PlaygroundEditor,
  readFile: (path: string) => Promise<string | null>,
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
        definition: {},
        typeDefinition: {},
        references: {},
        documentHighlight: {},
        signatureHelp: {},
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
  const openedDocs = new Set<string>()
  lsp.notify("textDocument/didOpen", {
    textDocument: {
      uri: MAIN_URI,
      languageId: LANGUAGE_ID,
      version: 1,
      text: model.getValue(),
    },
  })
  openedDocs.add(MAIN_URI)

  let version = 1
  model.onDidChangeContent(() => {
    version++
    lsp.notify("textDocument/didChange", {
      textDocument: { uri: MAIN_URI, version },
      contentChanges: [{ text: model.getValue() }],
    })
  })

  // Returns (creating if needed) a read-only model for a navigation target —
  // effect d.ts files and bundled lib.d.ts live only in the wasm FS.
  async function ensureModel(uri: monaco.Uri): Promise<monaco.editor.ITextModel | null> {
    const existing = monaco.editor.getModel(uri)
    if (existing) return existing
    const content = await readFile(uriToFsPath(uri))
    if (content === null) return null
    const created = monaco.editor.createModel(content, LANGUAGE_ID, uri)
    const uriString = uri.toString()
    if (!openedDocs.has(uriString)) {
      openedDocs.add(uriString)
      lsp.notify("textDocument/didOpen", {
        textDocument: { uri: uriString, languageId: "typescript", version: 1, text: content },
      })
    }
    return created
  }

  // Pre-resolve location targets so peek widgets can render previews, then
  // hand monaco plain {uri, range} pairs.
  async function toMonacoLocations(
    raw: LspLocation | LspLocation[] | LspLocationLink[] | null,
  ): Promise<monaco.languages.Location[]> {
    if (!raw) return []
    const list = Array.isArray(raw) ? raw : [raw]
    const locations = list.map(entry =>
      "targetUri" in entry
        ? { uri: entry.targetUri, range: entry.targetSelectionRange ?? entry.targetRange }
        : { uri: entry.uri, range: entry.range })
    const distinct = [...new Set(locations.map(l => l.uri))].slice(0, 16)
    await Promise.all(distinct.map(uri => ensureModel(monaco.Uri.parse(uri))))
    return locations.map(l => ({ uri: monaco.Uri.parse(l.uri), range: toMonacoRange(l.range) }))
  }

  // Cross-file navigation (go to definition into effect's d.ts, peek entries,
  // …): load the target from the wasm FS and show it read-only in the editor.
  monaco.editor.registerEditorOpener({
    openCodeEditor: (_source, resource, selectionOrPosition) => {
      const open = async () => {
        const target = await ensureModel(resource)
        if (!target) return
        playground.editor.setModel(target)
        playground.editor.updateOptions({ readOnly: target !== model })
        if (selectionOrPosition && "startLineNumber" in selectionOrPosition) {
          playground.editor.setSelection(selectionOrPosition)
          playground.editor.revealRangeInCenter(selectionOrPosition)
        } else if (selectionOrPosition) {
          playground.editor.setPosition(selectionOrPosition)
          playground.editor.revealPositionInCenter(selectionOrPosition)
        }
        playground.editor.focus()
      }
      void open()
      return true
    },
  })
  // Returning to the editable buffer (file bar / peek on main.ets).
  playground.editor.onDidChangeModel(event => {
    playground.editor.updateOptions({ readOnly: event.newModelUrl?.toString() !== MAIN_URI })
  })

  registerHover(lsp)
  registerCompletions(lsp, initResult.capabilities.completionProvider?.triggerCharacters)
  registerNavigation(lsp, toMonacoLocations)
  registerDocumentHighlights(lsp)
  registerSignatureHelp(lsp, initResult.capabilities.signatureHelpProvider)
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

interface LspLocation {
  uri: string
  range: LspRange
}

interface LspLocationLink {
  targetUri: string
  targetRange: LspRange
  targetSelectionRange?: LspRange
}

function registerHover(lsp: LspClient): void {
  monaco.languages.registerHoverProvider(LANGUAGE_ID, {
    provideHover: async (model, position) => {
      const result = await lsp.request<{
        contents: { kind: string; value: string } | string
        range?: LspRange
      } | null>("textDocument/hover", {
        textDocument: { uri: lspUri(model) },
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
        textDocument: { uri: lspUri(model) },
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
    provideDocumentSemanticTokens: async model => {
      const result = await lsp.request<{ data: number[] } | null>(
        "textDocument/semanticTokens/full",
        { textDocument: { uri: lspUri(model) } },
      )
      if (!result) return null
      return { data: new Uint32Array(result.data), resultId: undefined }
    },
    releaseDocumentSemanticTokens: () => {},
  })
}

function registerNavigation(
  lsp: LspClient,
  toMonacoLocations: (
    raw: LspLocation | LspLocation[] | LspLocationLink[] | null,
  ) => Promise<monaco.languages.Location[]>,
): void {
  const locationRequest = (method: string) => async (
    model: monaco.editor.ITextModel,
    position: monaco.Position,
  ) => {
    const result = await lsp.request<LspLocation | LspLocation[] | LspLocationLink[] | null>(
      method,
      { textDocument: { uri: lspUri(model) }, position: toLspPosition(position) },
    )
    return toMonacoLocations(result)
  }

  monaco.languages.registerDefinitionProvider(LANGUAGE_ID, {
    provideDefinition: locationRequest("textDocument/definition"),
  })
  monaco.languages.registerTypeDefinitionProvider(LANGUAGE_ID, {
    provideTypeDefinition: locationRequest("textDocument/typeDefinition"),
  })
  monaco.languages.registerImplementationProvider(LANGUAGE_ID, {
    provideImplementation: locationRequest("textDocument/implementation"),
  })
  monaco.languages.registerReferenceProvider(LANGUAGE_ID, {
    provideReferences: async (model, position, context) => {
      const result = await lsp.request<LspLocation[] | null>("textDocument/references", {
        textDocument: { uri: lspUri(model) },
        position: toLspPosition(position),
        context: { includeDeclaration: context.includeDeclaration },
      })
      return toMonacoLocations(result)
    },
  })
}

function registerDocumentHighlights(lsp: LspClient): void {
  monaco.languages.registerDocumentHighlightProvider(LANGUAGE_ID, {
    provideDocumentHighlights: async (model, position) => {
      const result = await lsp.request<
        Array<{ range: LspRange; kind?: number }> | null
      >("textDocument/documentHighlight", {
        textDocument: { uri: lspUri(model) },
        position: toLspPosition(position),
      })
      return (result ?? []).map(highlight => ({
        range: toMonacoRange(highlight.range),
        kind: highlight.kind === 2
          ? monaco.languages.DocumentHighlightKind.Read
          : highlight.kind === 3
            ? monaco.languages.DocumentHighlightKind.Write
            : monaco.languages.DocumentHighlightKind.Text,
      }))
    },
  })
}

interface LspSignatureHelp {
  signatures: Array<{
    label: string
    documentation?: { value: string } | string
    parameters?: Array<{ label: string | [number, number]; documentation?: { value: string } | string }>
  }>
  activeSignature?: number
  activeParameter?: number
}

function registerSignatureHelp(
  lsp: LspClient,
  serverCaps: { triggerCharacters?: string[]; retriggerCharacters?: string[] } | undefined,
): void {
  monaco.languages.registerSignatureHelpProvider(LANGUAGE_ID, {
    signatureHelpTriggerCharacters: serverCaps?.triggerCharacters ?? ["(", ","],
    signatureHelpRetriggerCharacters: serverCaps?.retriggerCharacters ?? [")"],
    provideSignatureHelp: async (model, position) => {
      const result = await lsp.request<LspSignatureHelp | null>("textDocument/signatureHelp", {
        textDocument: { uri: lspUri(model) },
        position: toLspPosition(position),
      })
      if (!result || result.signatures.length === 0) return null
      const toDoc = (doc: { value: string } | string | undefined) =>
        typeof doc === "string" ? doc : doc?.value
      return {
        value: {
          signatures: result.signatures.map(signature => ({
            label: signature.label,
            documentation: toDoc(signature.documentation),
            parameters: (signature.parameters ?? []).map(parameter => ({
              label: parameter.label,
              documentation: toDoc(parameter.documentation),
            })),
          })),
          activeSignature: result.activeSignature ?? 0,
          activeParameter: result.activeParameter ?? 0,
        },
        dispose: () => {},
      }
    },
  })
}
