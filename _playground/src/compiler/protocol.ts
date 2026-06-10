// Message types shared between the main thread and the compiler worker.
// Plain TS so the worker (glue code) can import it without the ets pipeline.

/** Mirrors internal/playground.Diagnostic (JSON). */
export interface CompileDiagnostic {
    file: string;
    pos: number;
    end: number;
    /** 0-based */
    line: number;
    /** 0-based, UTF-16 */
    character: number;
    code: number;
    category: string;
    message: string;
}

/** Mirrors internal/playground.OutputFile (JSON). */
export interface CompileOutputFile {
    name: string;
    text: string;
}

/** Mirrors internal/playground.CompileResult (JSON). */
export interface CompileResult {
    files: CompileOutputFile[];
    diagnostics: CompileDiagnostic[];
}

/** main thread -> worker (the LSP MessagePort is transferred with "init"). */
export type WorkerRequest =
    | { kind: "init"; source: string }
    | { kind: "compile"; id: number; source: string }
    | { kind: "read-file"; id: number; path: string };

/** worker -> main thread. */
export type WorkerResponse =
    | { kind: "ready" }
    | { kind: "fatal"; message: string }
    | { kind: "compile-result"; id: number; result?: CompileResult; error?: string }
    | { kind: "file-content"; id: number; content: string | null };

/** The in-wasm workspace; model URIs must match. */
export const WORKSPACE_ROOT = "/project";
export const MAIN_FILE = "/project/main.ets";
export const MAIN_URI = `file://${MAIN_FILE}`;

export const PLAYGROUND_TSCONFIG = JSON.stringify({
    compilerOptions: {
        module: "esnext",
        target: "es2022",
        moduleResolution: "bundler",
        strict: true,
        skipLibCheck: true,
        types: [],
    },
    files: ["main.ets"],
});
