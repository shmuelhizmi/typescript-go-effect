// One-command release flow for @playfast/tsagent:
//   npm run tsagent:release -- patch
//   npm run tsagent:release -- current   # retry publishing the current version
//
// Bumps _packages/tsagent/package.json, builds all platform packages, commits
// the current worktree plus the version bump, then publishes platform packages
// before the main package. npm login / publish approval stays interactive.

import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..", "..");
const packagePath = path.join(repoRoot, "_packages", "tsagent", "package.json");
const buildScript = path.join(repoRoot, "_packages", "tsagent", "scripts", "build.mjs");
const builtNpm = path.join(repoRoot, "built", "npm");

const platformDirs = [
    "tsagent-darwin-arm64",
    "tsagent-darwin-x64",
    "tsagent-linux-arm64",
    "tsagent-linux-x64",
    "tsagent-win32-arm64",
    "tsagent-win32-x64",
];

const args = process.argv.slice(2);
const opts = {
    bump: "patch",
    tag: "latest",
    otp: "",
    commit: true,
    publish: true,
    dryRun: false,
};

for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    switch (arg) {
        case "--tag":
            opts.tag = requireValue(args, ++i, arg);
            break;
        case "--otp":
            opts.otp = requireValue(args, ++i, arg);
            break;
        case "--no-commit":
            opts.commit = false;
            break;
        case "--no-publish":
            opts.publish = false;
            break;
        case "--dry-run":
            opts.dryRun = true;
            break;
        case "-h":
        case "--help":
            usage();
            process.exit(0);
            break;
        default:
            if (arg.startsWith("-")) {
                throw new Error(`Unknown flag: ${arg}`);
            }
            opts.bump = arg;
            break;
    }
}

const pkg = readJSON(packagePath);
const previousVersion = pkg.version;
const publishCurrent = opts.bump === "current" || opts.bump === "resume";
const nextVersion = publishCurrent ? previousVersion : nextPackageVersion(previousVersion, opts.bump);
const mainPackage = pkg.name;

console.log(publishCurrent ? `${mainPackage}: publishing current ${nextVersion}` : `${mainPackage}: ${previousVersion} -> ${nextVersion}`);

if (opts.publish && !opts.dryRun && !publishCurrent) {
    assertPackageVersionIsNew(mainPackage, nextVersion);
    for (const dir of platformDirs) {
        assertPackageVersionIsNew(`@playfast/${dir}`, nextVersion);
    }
}

if (!publishCurrent) {
    pkg.version = nextVersion;
    for (const dep of Object.keys(pkg.optionalDependencies ?? {})) {
        pkg.optionalDependencies[dep] = nextVersion;
    }
    writeJSON(packagePath, pkg, opts.dryRun);
}

run("node", [buildScript], { dryRun: opts.dryRun });

if (opts.commit && !publishCurrent) {
    run("git", ["add", "-A"], { dryRun: opts.dryRun });
    if (opts.dryRun || hasStagedChanges()) {
        run("git", ["commit", "-m", `tsagent: release v${nextVersion}`], { dryRun: opts.dryRun });
    }
    else {
        console.log("git commit skipped: no staged changes");
    }
}
else if (publishCurrent) {
    console.log("git commit skipped: current/resume mode");
}

if (opts.publish) {
    ensureNpmLogin(opts.dryRun);
    for (const dir of platformDirs) {
        publishPackage(path.join(builtNpm, dir), `@playfast/${dir}`, nextVersion, opts);
    }
    publishPackage(path.join(builtNpm, "tsagent"), mainPackage, nextVersion, opts);
}

console.log(`done: ${mainPackage}@${nextVersion}`);

function requireValue(values, index, flag) {
    const value = values[index];
    if (!value || value.startsWith("-")) {
        throw new Error(`${flag} requires a value`);
    }
    return value;
}

function usage() {
    console.log(`Usage: npm run tsagent:release -- [patch|minor|major|x.y.z|current] [flags]

Flags:
  --tag <name>     npm dist-tag to publish with (default: latest)
  --otp <code>     pass an npm one-time password to every publish command
  --no-commit      bump/build/publish without creating a git commit
  --no-publish     bump/build/commit without publishing
  --dry-run        print the release steps without changing files

The command commits the full current worktree plus the version bump.
Use "current" or "resume" to rebuild and publish the current package version without another bump.`);
}

function readJSON(file) {
    return JSON.parse(fs.readFileSync(file, "utf8"));
}

function writeJSON(file, value, dryRun) {
    const content = JSON.stringify(value, undefined, 4) + "\n";
    if (dryRun) {
        console.log(`[dry-run] write ${path.relative(repoRoot, file)}`);
        return;
    }
    fs.writeFileSync(file, content);
}

function nextPackageVersion(current, bump) {
    if (/^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(bump)) {
        return bump.replace(/^v/, "");
    }
    const match = current.match(/^(\d+)\.(\d+)\.(\d+)(?:-.+)?$/);
    if (!match) {
        throw new Error(`Cannot bump non-semver version ${current}`);
    }
    let [, major, minor, patch] = match.map(Number);
    switch (bump) {
        case "major":
            major++;
            minor = 0;
            patch = 0;
            break;
        case "minor":
            minor++;
            patch = 0;
            break;
        case "patch":
            patch++;
            break;
        default:
            throw new Error(`Unknown version bump ${bump}; use patch, minor, major, or x.y.z`);
    }
    return `${major}.${minor}.${patch}`;
}

function run(command, args, { dryRun = false } = {}) {
    console.log(`$ ${[command, ...args].map(shellQuote).join(" ")}`);
    if (dryRun) {
        return;
    }
    const result = spawnSync(command, args, { cwd: repoRoot, stdio: "inherit" });
    if (result.error) {
        throw result.error;
    }
    if (result.status !== 0) {
        throw new Error(`${command} exited with ${result.status}`);
    }
}

function output(command, args, { allowFailure = false } = {}) {
    try {
        return execFileSync(command, args, {
            cwd: repoRoot,
            encoding: "utf8",
            stdio: ["ignore", "pipe", "pipe"],
        }).trim();
    }
    catch (error) {
        if (allowFailure) {
            return "";
        }
        throw error;
    }
}

function assertPackageVersionIsNew(packageName, version) {
    if (packageVersionExists(packageName, version)) {
        throw new Error(`${packageName}@${version} already exists on npm`);
    }
}

function packageVersionExists(packageName, version) {
    return output("npm", ["view", `${packageName}@${version}`, "version"], { allowFailure: true }) === version;
}

function hasStagedChanges() {
    const result = spawnSync("git", ["diff", "--cached", "--quiet"], { cwd: repoRoot, stdio: "ignore" });
    return result.status === 1;
}

function ensureNpmLogin(dryRun) {
    console.log("$ npm whoami");
    if (dryRun) {
        return;
    }
    const whoami = output("npm", ["whoami"], { allowFailure: true });
    if (whoami) {
        console.log(`npm user: ${whoami}`);
        return;
    }
    run("npm", ["login"]);
    const loggedIn = output("npm", ["whoami"], { allowFailure: true });
    if (!loggedIn) {
        throw new Error("npm login did not complete");
    }
    console.log(`npm user: ${loggedIn}`);
}

function publishPackage(packageDir, expectedName, expectedVersion, { tag, otp, dryRun }) {
    const pkg = dryRun ? { name: expectedName, version: expectedVersion } : readJSON(path.join(packageDir, "package.json"));
    if (!dryRun && packageVersionExists(pkg.name, pkg.version)) {
        console.log(`publishing ${pkg.name}@${pkg.version}: already on npm, skipping`);
        return;
    }
    const args = ["publish", packageDir, "--access", "public", "--tag", tag];
    if (otp) {
        args.push("--otp", otp);
    }
    console.log(`publishing ${pkg.name}@${pkg.version}`);
    run("npm", args, { dryRun });
}

function shellQuote(value) {
    if (/^[A-Za-z0-9_./:=@+-]+$/.test(value)) {
        return value;
    }
    return JSON.stringify(value);
}
