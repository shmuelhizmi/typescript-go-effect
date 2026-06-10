import fs from "node:fs";
import module from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

export default function getExePath() {
    const expectedPackage = "@playfast/tsagent-" + process.platform + "-" + process.arch;

    const __dirname = path.dirname(fileURLToPath(import.meta.url));
    const normalizedDirname = __dirname.replace(/\\/g, "/");

    let exeDir;

    if (normalizedDirname.endsWith("/_packages/tsagent/lib")) {
        // Running from the repo: use the staged sibling platform package.
        exeDir = path.resolve(__dirname, "..", "..", "..", "built", "npm", "tsagent-" + process.platform + "-" + process.arch, "lib");
    }
    else if (normalizedDirname.endsWith("/built/npm/tsagent/lib")) {
        // Running from the staged (pre-publish) output: sibling platform dir.
        exeDir = path.resolve(__dirname, "..", "..", "tsagent-" + process.platform + "-" + process.arch, "lib");
    }
    else {
        // Running from an installed package: resolve the platform package
        // that npm selected via optionalDependencies os/cpu constraints.
        try {
            if (typeof import.meta.resolve === "undefined") {
                const require = module.createRequire(import.meta.url);
                const packageJson = require.resolve(expectedPackage + "/package.json");
                exeDir = path.join(path.dirname(packageJson), "lib");
            }
            else {
                const packageJson = import.meta.resolve(expectedPackage + "/package.json");
                exeDir = path.join(path.dirname(fileURLToPath(packageJson)), "lib");
            }
        }
        catch {
            throw new Error(
                "Unable to resolve " + expectedPackage + ". Either your platform ("
                    + process.platform + "-" + process.arch
                    + ") is unsupported, or optional dependencies were not installed (avoid --no-optional).",
            );
        }
    }

    let exe = path.join(exeDir, "tsagent");
    if (process.platform === "win32") {
        exe += ".exe";
        if (exe.length >= 248) {
            exe = "\\\\?\\" + exe;
        }
    }

    if (!fs.existsSync(exe)) {
        throw new Error("Executable not found: " + exe);
    }

    return exe;
}
