// Standalone smoke test for the tsgo.wasm playground module (no browser).
//
//   npx hereby tsgo:wasm && node _playground/smoke/smoke.mjs
//
// Drives the same JS surface the playground worker uses: init with a packed
// workspace (including the real `effect` type definitions from example/ets),
// an LSP session over the byte bridge, and a compile() call.
import fs from "node:fs";
import path from "node:path";
import url from "node:url";

const here = path.dirname(url.fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const wasmDir = path.join(repoRoot, "_playground/public/wasm");

// ---------------------------------------------------------------------------
// Boot the wasm module.
const t0 = performance.now();
await import(url.pathToFileURL(path.join(wasmDir, "wasm_exec.js")));
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(
    fs.readFileSync(path.join(wasmDir, "tsgo.wasm")),
    go.importObject,
);
void go.run(instance); // resolves only when the Go program exits; don't await
if (typeof globalThis.tsgoWasm !== "object") {
    throw new Error("tsgoWasm global was not registered");
}
const tsgo = globalThis.tsgoWasm;
log(`wasm booted in ${ms(t0)}`);

// ---------------------------------------------------------------------------
// Build the workspace pack: tsconfig + main.ets + effect type definitions.
const tPack = performance.now();
const mainEts = fs.readFileSync(path.join(repoRoot, "example/ets/src/main.ets"), "utf8");
const tsconfig = JSON.stringify({
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

const files = new Map();
files.set("/project/main.ets", mainEts);
files.set("/project/tsconfig.json", tsconfig);
for (const pkg of ["effect", "@standard-schema/spec", "fast-check", "pure-rand"]) {
    const pkgRoot = path.join(repoRoot, "example/ets/node_modules", pkg);
    if (!fs.existsSync(pkgRoot)) continue;
    for (const entry of fs.readdirSync(pkgRoot, { recursive: true, withFileTypes: true })) {
        if (!entry.isFile()) continue;
        const abs = path.join(entry.parentPath, entry.name);
        const rel = path.relative(pkgRoot, abs);
        if (rel.split(path.sep).includes("node_modules")) continue;
        const isDts = entry.name.endsWith(".d.ts") || entry.name.endsWith(".d.mts") || entry.name.endsWith(".d.cts");
        const isPkgJson = entry.name === "package.json";
        if (!isDts && !isPkgJson) continue;
        if (rel.startsWith(path.join("dist", "cjs"))) continue;
        files.set(`/project/node_modules/${pkg}/${rel.split(path.sep).join("/")}`, fs.readFileSync(abs, "utf8"));
    }
}

const encoder = new TextEncoder();
const manifest = { files: {} };
const chunks = [];
let offset = 0;
for (const [name, content] of files) {
    const bytes = encoder.encode(content);
    manifest.files[name] = [offset, bytes.length];
    chunks.push(bytes);
    offset += bytes.length;
}
const blob = new Uint8Array(offset);
{
    let pos = 0;
    for (const chunk of chunks) {
        blob.set(chunk, pos);
        pos += chunk.length;
    }
}
log(`packed ${files.size} files (${(offset / 1024 / 1024).toFixed(1)}MB) in ${ms(tPack)}`);

// ---------------------------------------------------------------------------
// LSP plumbing: Content-Length framing + a tiny request/notification client.
const pendingResponses = new Map(); // id -> resolve
const notificationWaiters = []; // {method, resolve}
let nextId = 1;
let recvBuffer = new Uint8Array(0);
const decoder = new TextDecoder();

tsgo.onLspMessage(bytes => {
    const merged = new Uint8Array(recvBuffer.length + bytes.length);
    merged.set(recvBuffer);
    merged.set(bytes, recvBuffer.length);
    recvBuffer = merged;
    for (;;) {
        const headerEnd = indexOfSeq(recvBuffer, [13, 10, 13, 10]);
        if (headerEnd < 0) return;
        const header = decoder.decode(recvBuffer.subarray(0, headerEnd));
        const len = Number(/Content-Length: *(\d+)/i.exec(header)?.[1]);
        const start = headerEnd + 4;
        if (recvBuffer.length < start + len) return;
        const msg = JSON.parse(decoder.decode(recvBuffer.subarray(start, start + len)));
        recvBuffer = recvBuffer.slice(start + len);
        handleMessage(msg);
    }
});

function handleMessage(msg) {
    if (msg.id !== undefined && msg.method === undefined) {
        pendingResponses.get(msg.id)?.(msg);
        pendingResponses.delete(msg.id);
        return;
    }
    if (msg.id !== undefined && msg.method !== undefined) {
        // Server -> client request; the playground client answers null to
        // capability registrations, mirror that here.
        send({ jsonrpc: "2.0", id: msg.id, result: null });
        return;
    }
    for (let i = 0; i < notificationWaiters.length; i++) {
        const waiter = notificationWaiters[i];
        if (waiter.method === msg.method && (!waiter.predicate || waiter.predicate(msg))) {
            notificationWaiters.splice(i, 1)[0].resolve(msg);
            return;
        }
    }
}

function send(msg) {
    const body = encoder.encode(JSON.stringify(msg));
    const head = encoder.encode(`Content-Length: ${body.length}\r\n\r\n`);
    const framed = new Uint8Array(head.length + body.length);
    framed.set(head);
    framed.set(body, head.length);
    tsgo.lspWrite(framed);
}

function request(method, params) {
    const id = nextId++;
    return withTimeout(`response to ${method}`, new Promise(resolve => {
        pendingResponses.set(id, resolve);
        send({ jsonrpc: "2.0", id, method, params });
    }));
}

function notify(method, params) {
    send({ jsonrpc: "2.0", method, params });
}

function waitForNotification(method, predicate) {
    return withTimeout(`notification ${method}`, new Promise(resolve => {
        notificationWaiters.push({ method, predicate, resolve });
    }));
}

function withTimeout(what, promise, timeoutMs = 120_000) {
    return Promise.race([
        promise,
        new Promise((_, reject) => setTimeout(() => reject(new Error(`timed out waiting for ${what}`)), timeoutMs).unref?.()),
    ]);
}

function indexOfSeq(buf, seq) {
    outer: for (let i = 0; i + seq.length <= buf.length; i++) {
        for (let j = 0; j < seq.length; j++) {
            if (buf[i + j] !== seq[j]) continue outer;
        }
        return i;
    }
    return -1;
}

// ---------------------------------------------------------------------------
// Scenario.
const tInit = performance.now();
await tsgo.init(JSON.stringify(manifest), blob);
log(`tsgoWasm.init done in ${ms(tInit)}`);

const tLsp = performance.now();
const initResult = await request("initialize", {
    processId: null,
    rootUri: "file:///project",
    workspaceFolders: [{ uri: "file:///project", name: "project" }],
    capabilities: {
        textDocument: {
            publishDiagnostics: {},
            diagnostic: {},
            hover: {},
            completion: { completionItem: {} },
            semanticTokens: {
                requests: { full: true },
                tokenTypes: [
                    "namespace", "class", "enum", "interface", "typeParameter",
                    "type", "parameter", "variable", "property", "enumMember",
                    "function", "method", "member",
                ],
                tokenModifiers: ["declaration", "static", "async", "readonly", "defaultLibrary", "local"],
                formats: ["relative"],
            },
        },
    },
});
assert(initResult.result?.capabilities?.semanticTokensProvider, "server should advertise semantic tokens");
notify("initialized", {});
log(`initialize round-trip in ${ms(tLsp)}`);

const tDiag = performance.now();
notify("textDocument/didOpen", {
    textDocument: {
        uri: "file:///project/main.ets",
        languageId: "effectscript",
        version: 1,
        text: mainEts,
    },
});
// File diagnostics are pull-based in tsgo (textDocument/diagnostic); only
// project/config diagnostics are pushed via publishDiagnostics.
const diagnostics = await request("textDocument/diagnostic", {
    textDocument: { uri: "file:///project/main.ets" },
});
assert(diagnostics.result?.kind === "full", `expected a full diagnostic report, got ${JSON.stringify(diagnostics.result).slice(0, 200)}`);
assert(
    Array.isArray(diagnostics.result.items) && diagnostics.result.items.length === 0,
    `quickstart should be clean, got: ${JSON.stringify(diagnostics.result?.items, null, 2)}`,
);
log(`first pull diagnostics (clean) in ${ms(tDiag)}`);

// Hover over `findUser` usage; quickstart has `user <- findUser("42") catch {`.
const hoverLine = mainEts.split("\n").findIndex(l => l.includes("user <- findUser"));
const tHover = performance.now();
const hover = await request("textDocument/hover", {
    textDocument: { uri: "file:///project/main.ets" },
    position: { line: hoverLine, character: mainEts.split("\n")[hoverLine].indexOf("findUser") + 2 },
});
assert(hover.result?.contents, "hover should return contents");
log(`hover in ${ms(tHover)}: ${JSON.stringify(hover.result.contents).slice(0, 120)}...`);

const tCompletion = performance.now();
const completion = await request("textDocument/completion", {
    textDocument: { uri: "file:///project/main.ets" },
    position: { line: hoverLine, character: mainEts.split("\n")[hoverLine].indexOf("findUser") + 2 },
});
const items = completion.result?.items ?? completion.result ?? [];
assert(items.length > 0, "completion should return items");
assert(items.some(i => i.label === "findUser"), "completions should include findUser");
log(`completion in ${ms(tCompletion)} (${items.length} items)`);

const tTokens = performance.now();
const tokens = await request("textDocument/semanticTokens/full", {
    textDocument: { uri: "file:///project/main.ets" },
});
if (!Array.isArray(tokens.result?.data)) {
    console.log("[debug] semanticTokens response:", JSON.stringify(tokens).slice(0, 400));
}
assert(Array.isArray(tokens.result?.data) && tokens.result.data.length > 0, "semantic tokens data");
log(`semanticTokens/full in ${ms(tTokens)} (${tokens.result.data.length / 5} tokens)`);

// ---------------------------------------------------------------------------
// Compile leg.
const tCompile = performance.now();
const edited = mainEts.replace("user-${id}", "user#${id}");
const writeError = tsgo.writeFile("/project/main.ets", edited);
assert(writeError === null, `writeFile error: ${writeError}`);
const compiled = await tsgo.compile();
assert(Array.isArray(compiled.files) && compiled.files.length === 1, "one emitted file");
assert(compiled.files[0].name === "/project/main.js", "emitted next to source");
assert(compiled.files[0].text.includes(`from "effect"`), "emitted JS imports effect");
assert(compiled.files[0].text.includes("user#"), "compile sees the edited text");
assert(compiled.diagnostics.length === 0, `no diagnostics, got ${JSON.stringify(compiled.diagnostics)}`);
log(`compile in ${ms(tCompile)} (${compiled.files[0].text.length} chars of JS)`);

log("ALL SMOKE TESTS PASSED");
process.exit(0);

function assert(cond, message) {
    if (!cond) {
        console.error(`FAIL: ${message}`);
        process.exit(1);
    }
}

function ms(since) {
    return `${(performance.now() - since).toFixed(0)}ms`;
}

function log(message) {
    console.log(`[smoke] ${message}`);
}
