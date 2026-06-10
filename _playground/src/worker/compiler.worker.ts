// Compiler worker: boots tsgo.wasm, loads the effect types pack into the
// in-memory workspace, then bridges LSP traffic (parsed JSON messages on a
// transferred MessagePort <-> Content-Length framed bytes for the Go server)
// and serves compile requests over the worker's own postMessage channel.
/// <reference lib="webworker" />
import {
    MAIN_FILE,
    PLAYGROUND_TSCONFIG,
    WORKSPACE_ROOT,
    type CompileResult,
    type WorkerRequest,
    type WorkerResponse,
} from "../compiler/protocol";
import { FrameDecoder, frame } from "./framing";

interface TsgoWasm {
    init(manifestJSON: string, blob: Uint8Array): Promise<void>;
    lspWrite(bytes: Uint8Array): void;
    onLspMessage(cb: (bytes: Uint8Array) => void): void;
    writeFile(path: string, content: string): string | null;
    compile(): Promise<CompileResult>;
}

declare global {
    // Provided by wasm_exec.js
    var Go: new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> };
    var tsgoWasm: TsgoWasm;
}

const post = (msg: WorkerResponse) => self.postMessage(msg);

let lspPort: MessagePort | null = null;
let bootPromise: Promise<void> | null = null;
let compileChain: Promise<unknown> = Promise.resolve();

self.onmessage = (event: MessageEvent<WorkerRequest>) => {
    const msg = event.data;
    switch (msg.kind) {
        case "init": {
            lspPort = event.ports[0] ?? null;
            bootPromise = boot(msg.source);
            void bootPromise.then(
                () => post({ kind: "ready" }),
                error => post({ kind: "fatal", message: String(error?.stack ?? error) }),
            );
            break;
        }
        case "compile": {
            // Serialize compiles behind boot; each writes the latest source
            // first. The client gates on "ready" too, but a queued request
            // must never race the wasm bring-up.
            compileChain = compileChain.then(async () => {
                try {
                    if (!bootPromise) throw new Error("compile before init");
                    await bootPromise;
                    const writeError = tsgoWasm.writeFile(MAIN_FILE, msg.source);
                    if (writeError) throw new Error(writeError);
                    const result = await tsgoWasm.compile();
                    post({ kind: "compile-result", id: msg.id, result });
                } catch (error) {
                    post({ kind: "compile-result", id: msg.id, error: String(error) });
                }
            });
            break;
        }
    }
};

async function boot(initialSource: string): Promise<void> {
    const [, pack] = await Promise.all([bootWasm(), fetchTypesPack()]);

    // The pack already uses tsgoWasm.init's manifest+blob format; append the
    // workspace files (tsconfig + the editor buffer) to it.
    const extra: Array<[string, string]> = [
        [`${WORKSPACE_ROOT}/tsconfig.json`, PLAYGROUND_TSCONFIG],
        [MAIN_FILE, initialSource],
    ];
    const encoder = new TextEncoder();
    const extraBytes = extra.map(([, content]) => encoder.encode(content));
    const blob = new Uint8Array(pack.blob.length + extraBytes.reduce((n, b) => n + b.length, 0));
    blob.set(pack.blob);
    let offset = pack.blob.length;
    extra.forEach(([path], i) => {
        pack.manifest.files[path] = [offset, extraBytes[i].length];
        blob.set(extraBytes[i], offset);
        offset += extraBytes[i].length;
    });

    await tsgoWasm.init(JSON.stringify(pack.manifest), blob);

    if (!lspPort) throw new Error("init message carried no LSP MessagePort");
    const port = lspPort;
    const frameDecoder = new FrameDecoder();
    tsgoWasm.onLspMessage(bytes => frameDecoder.push(bytes, message => port.postMessage(message)));
    port.onmessage = e => tsgoWasm.lspWrite(frame(e.data));
}

async function bootWasm(): Promise<void> {
    const wasmExecUrl = new URL("/wasm/wasm_exec.js", self.location.origin).href;
    try {
        await import(/* @vite-ignore */ wasmExecUrl);
    } catch (error) {
        throw new Error(
            `failed to load ${wasmExecUrl} (${error}) — build the wasm artifacts with "npm run build:wasm"`,
        );
    }
    const go = new Go();
    const { instance } = await WebAssembly.instantiateStreaming(
        fetchOk("/wasm/tsgo.wasm"),
        go.importObject,
    );
    void go.run(instance); // resolves only if the Go program exits
    if (typeof globalThis.tsgoWasm !== "object") {
        throw new Error("tsgo.wasm did not register globalThis.tsgoWasm");
    }
}

/** fetch() with the failing URL (and remedy) baked into the error message. */
async function fetchOk(url: string): Promise<Response> {
    let response: Response;
    try {
        response = await fetch(url);
    } catch (error) {
        throw new Error(`failed to fetch ${url}: ${error}`);
    }
    if (!response.ok) {
        throw new Error(
            `failed to fetch ${url}: HTTP ${response.status} — run "npm run build:wasm" and "npm run prepare:assets" (npm run dev does both)`,
        );
    }
    return response;
}

interface TypesPack {
    manifest: { files: Record<string, [number, number]> };
    blob: Uint8Array;
}

async function fetchTypesPack(): Promise<TypesPack> {
    const response = await fetchOk("/types-pack.bin.gz");
    const stream = response.body!.pipeThrough(new DecompressionStream("gzip"));
    const raw = new Uint8Array(await new Response(stream).arrayBuffer());
    const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength);
    const manifestLength = view.getUint32(0, true);
    const manifest = JSON.parse(new TextDecoder().decode(raw.subarray(4, 4 + manifestLength)));
    const body = raw.subarray(4 + manifestLength);
    // Manifest offsets are relative to the body start; keep them as-is and
    // hand the body straight to init.
    return { manifest, blob: body };
}
