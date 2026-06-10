// Builds tsgo.wasm + wasm_exec.js into public/wasm/ using the Go toolchain
// directly (no repo-root npm install needed). Mirrors the hereby tsgo:wasm
// task; go's build cache makes repeat runs near-instant.
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import url from "node:url";

const root = path.resolve(path.dirname(url.fileURLToPath(import.meta.url)), "..");
const outDir = path.join(root, "public/wasm");
fs.mkdirSync(outDir, { recursive: true });

const t0 = performance.now();
execFileSync(
    "go",
    ["build", "-trimpath", "-ldflags=-s -w", "-o", path.join(outDir, "tsgo.wasm"), "./cmd/tsgo-wasm"],
    {
        cwd: path.resolve(root, ".."),
        env: { ...process.env, GOOS: "js", GOARCH: "wasm", CGO_ENABLED: "0" },
        stdio: "inherit",
    },
);

const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
const wasmExecOut = path.join(outDir, "wasm_exec.js");
// The module-cache source is read-only; replace rather than overwrite.
fs.rmSync(wasmExecOut, { force: true });
fs.copyFileSync(path.join(goroot, "lib/wasm/wasm_exec.js"), wasmExecOut);
fs.chmodSync(wasmExecOut, 0o644);

const size = fs.statSync(path.join(outDir, "tsgo.wasm")).size;
console.log(`[build-wasm] tsgo.wasm ${(size / 1024 / 1024).toFixed(1)}MB in ${((performance.now() - t0) / 1000).toFixed(1)}s`);
