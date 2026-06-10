// Stages the @playfast/tsagent npm packages into built/npm/:
//   built/npm/tsagent/                  — main package (shim + README)
//   built/npm/tsagent-<os>-<arch>/      — one per platform (binary only)
//
// Usage: node _packages/tsagent/scripts/build.mjs [--current-only]
//   --current-only  build just the host platform (fast local smoke)

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..", "..");
const pkgSrc = path.resolve(__dirname, "..");
const builtNpm = path.join(repoRoot, "built", "npm");

const mainPkg = JSON.parse(fs.readFileSync(path.join(pkgSrc, "package.json"), "utf8"));
const version = mainPkg.version;

const platforms = [
    { os: "darwin", arch: "arm64", goos: "darwin", goarch: "arm64" },
    { os: "darwin", arch: "x64", goos: "darwin", goarch: "amd64" },
    { os: "linux", arch: "arm64", goos: "linux", goarch: "arm64" },
    { os: "linux", arch: "x64", goos: "linux", goarch: "amd64" },
    { os: "win32", arch: "arm64", goos: "windows", goarch: "arm64" },
    { os: "win32", arch: "x64", goos: "windows", goarch: "amd64" },
];

const currentOnly = process.argv.includes("--current-only");
const selected = currentOnly
    ? platforms.filter(p => p.os === process.platform && p.arch === process.arch)
    : platforms;

if (selected.length === 0) {
    throw new Error(`No platform entry for host ${process.platform}-${process.arch}`);
}

const optionalDependencies = {};

for (const p of selected) {
    const dirName = `tsagent-${p.os}-${p.arch}`;
    const pkgName = `@playfast/${dirName}`;
    const outDir = path.join(builtNpm, dirName);
    const libDir = path.join(outDir, "lib");
    fs.rmSync(outDir, { recursive: true, force: true });
    fs.mkdirSync(libDir, { recursive: true });

    const exe = path.join(libDir, p.os === "win32" ? "tsagent.exe" : "tsagent");
    console.log(`building ${pkgName}@${version} (${p.goos}/${p.goarch})…`);
    execFileSync("go", ["build", "-ldflags=-s -w", "-o", exe, "./cmd/tsagent"], {
        cwd: repoRoot,
        stdio: "inherit",
        env: { ...process.env, GOOS: p.goos, GOARCH: p.goarch, CGO_ENABLED: "0" },
    });

    fs.writeFileSync(
        path.join(outDir, "package.json"),
        JSON.stringify(
            {
                name: pkgName,
                version,
                license: mainPkg.license,
                description: `${p.os}-${p.arch} binary for @playfast/tsagent.`,
                repository: mainPkg.repository,
                publishConfig: { access: "public" },
                preferUnplugged: true,
                os: [p.os],
                cpu: [p.arch],
                files: ["lib"],
            },
            undefined,
            4,
        ) + "\n",
    );
    fs.writeFileSync(
        path.join(outDir, "README.md"),
        `# ${pkgName}\n\nPlatform binary for [\`@playfast/tsagent\`](https://www.npmjs.com/package/@playfast/tsagent). Install that package instead.\n`,
    );

    optionalDependencies[pkgName] = version;
}

// Stage the main package.
const mainOut = path.join(builtNpm, "tsagent");
fs.rmSync(mainOut, { recursive: true, force: true });
fs.mkdirSync(mainOut, { recursive: true });
fs.cpSync(path.join(pkgSrc, "bin"), path.join(mainOut, "bin"), { recursive: true });
fs.cpSync(path.join(pkgSrc, "lib"), path.join(mainOut, "lib"), { recursive: true });
// The CLI manual is the package README so the npm page documents every command.
fs.copyFileSync(path.join(repoRoot, "cmd", "tsagent", "README.md"), path.join(mainOut, "README.md"));

const staged = { ...mainPkg };
delete staged.private;
staged.optionalDependencies = currentOnly
    ? { ...mainPkg.optionalDependencies, ...optionalDependencies }
    : optionalDependencies;
fs.writeFileSync(path.join(mainOut, "package.json"), JSON.stringify(staged, undefined, 4) + "\n");

console.log(`\nstaged ${selected.length} platform package(s) + main package in built/npm/`);
console.log(`publish order: platform packages first, then built/npm/tsagent`);
