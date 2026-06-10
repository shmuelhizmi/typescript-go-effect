// Bundles the effect runtime (ESM, code-split) for the run-sandbox iframe and
// emits an import map covering the root import plus every subpath export, so
// transpiled playground programs can `import { Effect } from "effect"` (or
// any "effect/Module") natively in the browser.
import esbuild from "esbuild";
import fs from "node:fs";
import path from "node:path";
import url from "node:url";

const root = path.resolve(path.dirname(url.fileURLToPath(import.meta.url)), "..");
const outDir = path.join(root, "public/vendor/effect");

const pkg = JSON.parse(fs.readFileSync(path.join(root, "node_modules/effect/package.json"), "utf8"));

const entryPoints = {};
const importMap = { imports: {} };
for (const key of Object.keys(pkg.exports)) {
    if (key === "./package.json" || key === "./.index") continue;
    const specifier = key === "." ? "effect" : `effect/${key.slice(2)}`;
    const outName = key === "." ? "index" : key.slice(2);
    entryPoints[outName] = specifier;
    importMap.imports[specifier] = `/vendor/effect/${outName}.js`;
}

await fs.promises.rm(outDir, { recursive: true, force: true });
const result = await esbuild.build({
    entryPoints,
    bundle: true,
    splitting: true,
    format: "esm",
    outdir: outDir,
    chunkNames: "chunks/[name]-[hash]",
    minify: true,
    sourcemap: false,
    logLevel: "error",
    absWorkingDir: root,
    metafile: true,
});

fs.writeFileSync(path.join(outDir, "importmap.json"), JSON.stringify(importMap, null, 2));

const total = Object.values(result.metafile.outputs).reduce((sum, o) => sum + o.bytes, 0);
console.log(
    `[effect-vendor] ${Object.keys(entryPoints).length} entries -> ${(total / 1024 / 1024).toFixed(1)}MB in ${path.relative(root, outDir)}`,
);
