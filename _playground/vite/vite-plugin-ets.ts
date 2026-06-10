// Vite plugin that compiles .ets/.etsx modules with the native tsgo binary
// built from this repository — the playground app dogfoods the EffectScript
// compiler it showcases.
//
// tsgo has no single-file transpile mode, so the whole tsconfig.app.json
// project is compiled (with --noCheck for speed) into a cache directory and
// the transform pipeline serves the emitted JS from there. Any .ets/.etsx
// change recompiles the project and triggers a full reload; React fast
// refresh cannot see through pre-transpiled sources, so a reload is the
// honest behavior.
import { execFile } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import type { Plugin, ViteDevServer } from "vite";

const execFileAsync = promisify(execFile);

export interface EtsPluginOptions {
    /** Path to the native tsgo binary. Env TSGO_BIN overrides. */
    tsgoBin?: string;
    /** Project to compile, relative to the playground root. */
    project?: string;
    /** Emitted-JS cache directory (must match the project's outDir). */
    cacheDir?: string;
}

interface TsgoDiagnostic {
    file: string;
    line: number;
    column: number;
    code: string;
    message: string;
}

export function ets(options: EtsPluginOptions = {}): Plugin {
    const root = process.cwd();
    const tsgoBin = process.env.TSGO_BIN
        ?? options.tsgoBin
        ?? path.join(root, "node_modules/.cache/tsgo-bin/tsgo");
    const project = options.project ?? "tsconfig.app.json";
    const cacheDir = path.resolve(root, options.cacheDir ?? "node_modules/.ets-cache");
    const srcDir = path.resolve(root, "src");

    let compilePromise: Promise<TsgoDiagnostic[]> | null = null;
    let lastDiagnostics: TsgoDiagnostic[] = [];
    let recompileTimer: ReturnType<typeof setTimeout> | undefined;

    async function compile(): Promise<TsgoDiagnostic[]> {
        if (!fs.existsSync(tsgoBin)) {
            throw new Error(
                `tsgo binary not found at ${tsgoBin}; run "npm run build:tsgo" (or set TSGO_BIN)`,
            );
        }
        try {
            await execFileAsync(tsgoBin, ["-p", project, "--noCheck"], { cwd: root });
            return [];
        } catch (error) {
            const out = `${(error as { stdout?: string }).stdout ?? ""}\n${(error as { stderr?: string }).stderr ?? ""}`;
            const diagnostics = parseDiagnostics(out);
            if (diagnostics.length === 0) {
                throw new Error(`tsgo failed without parseable diagnostics:\n${out}`);
            }
            return diagnostics;
        }
    }

    function ensureCompiled(): Promise<TsgoDiagnostic[]> {
        compilePromise ??= compile().then(diagnostics => {
            lastDiagnostics = diagnostics;
            return diagnostics;
        });
        return compilePromise;
    }

    function scheduleRecompile(server: ViteDevServer) {
        clearTimeout(recompileTimer);
        recompileTimer = setTimeout(() => {
            compilePromise = null;
            void ensureCompiled().then(() => {
                server.ws.send({ type: "full-reload" });
            });
        }, 50);
    }

    return {
        name: "vite-plugin-ets",
        enforce: "pre",

        async buildStart() {
            await ensureCompiled();
        },

        resolveId(source, importer) {
            // Resolve extensionless relative imports that point at .ets/.etsx
            // files (Vite only probes .ts/.tsx/.js/... by default).
            if (!importer || !source.startsWith(".") || source.includes("?")) return null;
            const base = path.resolve(path.dirname(importer.split("?")[0]), source);
            if (path.extname(base) !== "") return null;
            for (const ext of [".ets", ".etsx"]) {
                if (fs.existsSync(base + ext)) return base + ext;
            }
            return null;
        },

        async load(id) {
            if (id.includes("?") || !/\.(ets|etsx)$/.test(id)) return null;
            const absolute = path.resolve(id);
            if (!absolute.startsWith(srcDir + path.sep)) return null;

            const diagnostics = await ensureCompiled();
            const mine = diagnostics.filter(d => path.resolve(root, d.file) === absolute);
            if (mine.length > 0) {
                const d = mine[0];
                this.error({
                    message: `tsgo ${d.code}: ${d.message}`,
                    loc: { file: absolute, line: d.line, column: d.column },
                });
            }

            const emitted = path.join(
                cacheDir,
                path.relative(srcDir, absolute).replace(/\.(ets|etsx)$/, ".js"),
            );
            if (!fs.existsSync(emitted)) {
                const other = diagnostics[0];
                this.error(
                    other
                        ? `tsgo emitted nothing for ${path.relative(root, absolute)}; first project error: ${other.file}(${other.line},${other.column}): ${other.code}: ${other.message}`
                        : `tsgo emitted nothing for ${path.relative(root, absolute)} (looked at ${emitted})`,
                );
            }
            const code = await fs.promises.readFile(emitted, "utf8");
            let map: string | undefined;
            try {
                map = await fs.promises.readFile(emitted + ".map", "utf8");
            } catch {
                // no sourcemap emitted
            }
            return { code, map };
        },

        handleHotUpdate({ file, server }) {
            if (!/\.(ets|etsx)$/.test(file)) return;
            scheduleRecompile(server);
            return [];
        },
    };
}

function parseDiagnostics(output: string): TsgoDiagnostic[] {
    // tsgo error lines look like: src/App.etsx(12,5): error TS1005: ',' expected.
    const diagnostics: TsgoDiagnostic[] = [];
    const pattern = /^(.+?)\((\d+),(\d+)\): error (TS\d+): (.*)$/gm;
    for (const match of output.matchAll(pattern)) {
        diagnostics.push({
            file: match[1],
            line: Number(match[2]),
            column: Number(match[3]),
            code: match[4],
            message: match[5],
        });
    }
    return diagnostics;
}
