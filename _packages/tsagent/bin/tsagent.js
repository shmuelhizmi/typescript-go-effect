#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import getExePath from "../lib/getExePath.js";

const exe = getExePath();

if (process.platform !== "win32" && typeof process.execve === "function") {
    // Node >= 22.15: replace the process entirely so signals/exit codes
    // pass through without a wrapper process.
    try {
        process.execve(exe, [exe, ...process.argv.slice(2)]);
    }
    catch {
        // execve may be unavailable; fall through to execFileSync.
    }
}

try {
    execFileSync(exe, process.argv.slice(2), { stdio: "inherit" });
}
catch (e) {
    if (e.status) {
        process.exitCode = e.status;
    }
    else {
        throw e;
    }
}
