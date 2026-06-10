// Hover-quality probe: boots tsgo.wasm, opens the playground default program,
// and prints quickinfo for a list of symbols. Used to chase "hover shows any".
//
//   node smoke/hover-probe.mjs
import fs from "node:fs";
import path from "node:path";
import url from "node:url";

const here = path.dirname(url.fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const wasmDir = path.join(repoRoot, "_playground/public/wasm");

await import(url.pathToFileURL(path.join(wasmDir, "wasm_exec.js")));
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(
    fs.readFileSync(path.join(wasmDir, "tsgo.wasm")),
    go.importObject,
);
void go.run(instance);
const tsgo = globalThis.tsgoWasm;

const source = `import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}

service Greeter {
  greeting: Effect.Effect<string>
}

layer GreeterLive: Greeter {
  return { greeting: Effect.succeed("Hello") }
}

effect findUser(id: string): string raises NotFound {
  if (id === "") raise new NotFound({ id })
  return \`user-\${id}\`
}

effect greet(name: string): string requires Greeter {
  greeter <- Greeter
  prefix  <- greeter.greeting
  return \`\${prefix}, \${name}!\`
}

effect main(): string requires Greeter {
  user <- findUser("42") catch {
    NotFound as e >> \`fallback-\${e.id}\`
  }

  [a, b] <- par [greet(user), greet("world")]

  defer { <- Effect.log("done") }

  return \`\${a} | \${b}\`
}

main()
  |> Effect.scoped
  |> Effect.provide(GreeterLive)
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
`;

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
files.set("/project/main.ets", source);
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
        if (!isDts && entry.name !== "package.json") continue;
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

const pendingResponses = new Map();
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
        if (msg.id !== undefined && msg.method === undefined) {
            pendingResponses.get(msg.id)?.(msg);
            pendingResponses.delete(msg.id);
        } else if (msg.id !== undefined) {
            send({ jsonrpc: "2.0", id: msg.id, result: null });
        }
    }
});

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
    return new Promise(resolve => {
        pendingResponses.set(id, resolve);
        send({ jsonrpc: "2.0", id, method, params });
    });
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

await tsgo.init(JSON.stringify(manifest), blob);
await request("initialize", {
    processId: null,
    rootUri: "file:///project",
    workspaceFolders: [{ uri: "file:///project", name: "project" }],
    capabilities: { textDocument: { hover: {}, diagnostic: {} } },
});
send({ jsonrpc: "2.0", method: "initialized", params: {} });
send({
    jsonrpc: "2.0",
    method: "textDocument/didOpen",
    params: {
        textDocument: { uri: "file:///project/main.ets", languageId: "effectscript", version: 1, text: source },
    },
});
const diag = await request("textDocument/diagnostic", { textDocument: { uri: "file:///project/main.ets" } });
console.log(`diagnostics: ${diag.result?.items?.length ?? "?"}`);

const lines = source.split("\n");
// [needle-line-substring, symbol, occurrence offset within symbol]
const targets = [
    ["service Greeter", "service"],
    ["layer GreeterLive", "layer"],
    ["effect findUser", "effect"],
    ["effect greet", "effect"],
    ["raises NotFound", "raises"],
    ["requires Greeter {", "requires"],
    ["if (id === \"\") raise", "raise"],
    ["greeter <- Greeter", "<-"],
    ["[a, b] <- par", "par"],
    ["defer {", "defer"],
    ["NotFound as e", "as"],
    ["NotFound as e", ">>"],
    ["|> Effect.scoped", "|>"],
    ["service Greeter", "Greeter"],
    ["greeting: Effect.Effect<string>", "greeting"],
    ["layer GreeterLive", "GreeterLive"],
    ["effect findUser", "findUser"],
    ["effect findUser", "id"],
    ["effect greet", "greet"],
    ["effect greet", "name"],
    ["greeter <- Greeter", "greeter"],
    ["greeter <- Greeter", "Greeter"],
    ["prefix  <- greeter.greeting", "prefix"],
    ["prefix  <- greeter.greeting", "greeting"],
    ["effect main", "main"],
    ["user <- findUser", "user"],
    ["user <- findUser", "findUser"],
    ["NotFound as e", "e"],
    ["[a, b] <- par", "a"],
    ["[a, b] <- par", "b"],
    ["greet(user)", "greet"],
    ["return \\`\\${a} | \\${b}\\`", "a"],
    ["main()", "main"],
    ["Effect.provide(GreeterLive)", "GreeterLive"],
];

for (const [needle, symbol] of targets) {
    const line = lines.findIndex(l => l.includes(needle));
    if (line < 0) {
        console.log(`SKIP (needle not found): ${needle}`);
        continue;
    }
    const character = lines[line].indexOf(symbol, lines[line].indexOf(needle.includes(symbol) ? needle.slice(0, needle.indexOf(symbol)) : "")) ;
    const col = lines[line].indexOf(symbol);
    const hover = await request("textDocument/hover", {
        textDocument: { uri: "file:///project/main.ets" },
        position: { line, character: col + 1 },
    });
    const contents = hover.result?.contents;
    const text = typeof contents === "string" ? contents : contents?.value ?? JSON.stringify(contents);
    const firstLines = (text ?? "<no hover>").split("\n").filter(l => l && !l.startsWith("```")).slice(0, 2).join(" ⏎ ");
    console.log(`${String(line + 1).padStart(3)}:${String(col + 1).padStart(3)} ${symbol.padEnd(12)} ${firstLines}`);
}
process.exit(0);
