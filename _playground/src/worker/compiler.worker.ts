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
let compileChain: Promise<unknown> = Promise.resolve();

self.onmessage = (event: MessageEvent<WorkerRequest>) => {
    const msg = event.data;
    switch (msg.kind) {
        case "init": {
            lspPort = event.ports[0] ?? null;
            void boot(msg.source).then(
                () => post({ kind: "ready" }),
                error => post({ kind: "fatal", message: String(error?.stack ?? error) }),
            );
            break;
        }
        case "compile": {
            // Serialize compiles; each writes the latest source first.
            compileChain = compileChain.then(async () => {
                try {
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
    await import(/* @vite-ignore */ new URL("/wasm/wasm_exec.js", self.location.origin).href);
    const go = new Go();
    const { instance } = await WebAssembly.instantiateStreaming(fetch("/wasm/tsgo.wasm"), go.importObject);
    void go.run(instance); // resolves only if the Go program exits
    if (typeof globalThis.tsgoWasm !== "object") {
        throw new Error("tsgo.wasm did not register globalThis.tsgoWasm");
    }
}

interface TypesPack {
    manifest: { files: Record<string, [number, number]> };
    blob: Uint8Array;
}

async function fetchTypesPack(): Promise<TypesPack> {
    const response = await fetch("/types-pack.bin.gz");
    if (!response.ok) throw new Error(`types pack fetch failed: ${response.status}`);
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
