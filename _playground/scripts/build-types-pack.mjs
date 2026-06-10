// Packs the type definitions the wasm compiler needs (effect + its type
// dependencies) into a single gzipped asset the worker fetches at startup.
//
// Format (after gunzip): [4-byte LE manifest length][manifest JSON][bodies]
// where manifest = {"files": {"/project/node_modules/...": [offset, length]}}
// — exactly the shape tsgoWasm.init() consumes, no re-encoding in the worker.
import fs from "node:fs";
import path from "node:path";
import url from "node:url";
import zlib from "node:zlib";

const root = path.resolve(path.dirname(url.fileURLToPath(import.meta.url)), "..");
// Gzipped content, but NOT a .gz extension: Vite (and many static hosts)
// serve *.gz with Content-Encoding: gzip, making the browser transparently
// decompress and breaking the worker's explicit DecompressionStream.
const outFile = path.join(root, "public/types-pack.bin");

const packages = ["effect", "@standard-schema/spec", "fast-check", "pure-rand"];
const encoder = new TextEncoder();
const manifest = { files: {} };
const chunks = [];
let offset = 0;
let count = 0;

for (const pkg of packages) {
    const pkgRoot = path.join(root, "node_modules", pkg);
    if (!fs.existsSync(pkgRoot)) {
        console.error(`missing package ${pkg}; run npm install first`);
        process.exit(1);
    }
    for (const entry of fs.readdirSync(pkgRoot, { recursive: true, withFileTypes: true })) {
        if (!entry.isFile()) continue;
        const abs = path.join(entry.parentPath, entry.name);
        const rel = path.relative(pkgRoot, abs);
        const parts = rel.split(path.sep);
        if (parts.includes("node_modules")) continue;
        // Only type information matters in the wasm workspace: declarations
        // and package.json files for module resolution. Skip the runtime
        // dists and declaration maps.
        const isDts = /\.d\.(ts|mts|cts)$/.test(entry.name);
        const isPkgJson = entry.name === "package.json";
        if (!isDts && !isPkgJson) continue;
        if (parts[0] === "dist" && (parts[1] === "cjs" || parts[1] === "esm")) continue;
        const bytes = encoder.encode(fs.readFileSync(abs, "utf8"));
        manifest.files[`/project/node_modules/${pkg}/${parts.join("/")}`] = [offset, bytes.length];
        chunks.push(bytes);
        offset += bytes.length;
        count++;
    }
}

const manifestBytes = encoder.encode(JSON.stringify(manifest));
const header = Buffer.alloc(4);
header.writeUInt32LE(manifestBytes.length);
const raw = Buffer.concat([header, manifestBytes, ...chunks]);
const compressed = zlib.gzipSync(raw, { level: 9 });
fs.mkdirSync(path.dirname(outFile), { recursive: true });
fs.writeFileSync(outFile, compressed);

console.log(
    `[types-pack] ${count} files, ${(raw.length / 1024 / 1024).toFixed(1)}MB raw -> ${(compressed.length / 1024 / 1024).toFixed(2)}MB gz at ${path.relative(root, outFile)}`,
);
