// Runs repeatable tsagent memory probes with macOS /usr/bin/time -l.
//
// Usage:
//   node _packages/tsagent/scripts/measure-memory.mjs --project /path/to/tsconfig.json
//   node _packages/tsagent/scripts/measure-memory.mjs --project /path/to/tsconfig.json --heavy
//   node _packages/tsagent/scripts/measure-memory.mjs --project /path/to/tsconfig.json --bin /tmp/tsagent-opt --probe quality --probe perf
//   node _packages/tsagent/scripts/measure-memory.mjs --project /path/to/tsconfig.json --baseline-bin /tmp/old --candidate-bin /tmp/new --heavy

import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..", "..");

const allProbes = [
    {
        name: "map-files-limit1",
        group: "small",
        args: ["map", "files", "--limit", "1"],
    },
    {
        name: "report-structure-top1",
        group: "small",
        args: ["report", "structure", "--top", "1"],
        html: true,
    },
    {
        name: "report-assertions-top1",
        group: "small",
        args: ["report", "--include", "assertions", "--top", "1"],
        html: true,
    },
    {
        name: "report-complexity-top1",
        group: "small",
        args: ["report", "--include", "complexity", "--top", "1"],
        html: true,
    },
    {
        name: "report-quality-top1",
        group: "small",
        args: ["report", "quality", "--top", "1"],
        html: true,
    },
    {
        name: "perf-summary",
        group: "heavy",
        args: ["perf", "summary"],
    },
    {
        name: "report-perf-top1",
        group: "heavy",
        args: ["report", "perf", "--top", "1"],
        html: true,
    },
    {
        name: "report-include-perf-top1",
        group: "heavy",
        args: ["report", "--include", "perf", "--top", "1"],
        html: true,
    },
    {
        name: "report-full-top1",
        group: "heavy",
        args: ["report", "full", "--top", "1"],
        html: true,
    },
];

const opts = parseArgs(process.argv.slice(2));
if (opts.help || !opts.project) {
    printUsage();
    process.exit(opts.help ? 0 : 2);
}

const timeBin = "/usr/bin/time";
if (!fs.existsSync(timeBin)) {
    throw new Error(`${timeBin} is required; this harness currently parses macOS time -l output`);
}

const outDir = path.resolve(opts.outDir ?? path.join(os.tmpdir(), "tsagent-memory-probes"));
fs.mkdirSync(outDir, { recursive: true });

const selected = selectProbes(opts);

if (opts.baselineBin || opts.candidateBin) {
    if (!opts.baselineBin || !opts.candidateBin) {
        throw new Error("--baseline-bin and --candidate-bin must be provided together");
    }
    const baselineRows = runSuite({
        label: "baseline",
        bin: path.resolve(opts.baselineBin),
        project: opts.project,
        outDir: path.join(outDir, "baseline"),
        selected,
    });
    const candidateRows = runSuite({
        label: "candidate",
        bin: path.resolve(opts.candidateBin),
        project: opts.project,
        outDir: path.join(outDir, "candidate"),
        selected,
    });
    writeComparison(outDir, baselineRows, candidateRows);
    console.log(`\nwrote ${path.join(outDir, "comparison.md")}`);
} else {
    const bin = opts.bin ? path.resolve(opts.bin) : buildBinary(outDir);
    const rows = runSuite({ label: "", bin, project: opts.project, outDir, selected });
    writeSummary(outDir, rows);
    console.log(`\nwrote ${path.join(outDir, "summary.md")}`);
}

function parseArgs(args) {
    const opts = { probes: [] };
    for (let i = 0; i < args.length; i++) {
        const arg = args[i];
        switch (arg) {
            case "--help":
            case "-h":
                opts.help = true;
                break;
            case "--project":
                opts.project = requireValue(args, ++i, arg);
                break;
            case "--bin":
                opts.bin = requireValue(args, ++i, arg);
                break;
            case "--baseline-bin":
                opts.baselineBin = requireValue(args, ++i, arg);
                break;
            case "--candidate-bin":
                opts.candidateBin = requireValue(args, ++i, arg);
                break;
            case "--out-dir":
                opts.outDir = requireValue(args, ++i, arg);
                break;
            case "--probe":
                opts.probes.push(requireValue(args, ++i, arg));
                break;
            case "--heavy":
                opts.heavy = true;
                break;
            case "--list":
                opts.list = true;
                break;
            default:
                throw new Error(`unknown argument: ${arg}`);
        }
    }
    if (opts.list) {
        for (const p of allProbes) {
            console.log(`${p.name}\t${p.group}`);
        }
        process.exit(0);
    }
    return opts;
}

function requireValue(args, index, flag) {
    if (index >= args.length || args[index].startsWith("--")) {
        throw new Error(`${flag} requires a value`);
    }
    return args[index];
}

function printUsage() {
    console.log(`Usage: node _packages/tsagent/scripts/measure-memory.mjs --project <tsconfig> [--bin <path>] [--out-dir <dir>] [--heavy] [--probe <name>...]`);
    console.log(`       node _packages/tsagent/scripts/measure-memory.mjs --project <tsconfig> --baseline-bin <path> --candidate-bin <path> [--heavy] [--probe <name>...]`);
    console.log("\nDefault probes are small. Add --heavy for perf/full, or select probes explicitly with --probe.");
}

function buildBinary(outDir) {
    const bin = path.join(outDir, `tsagent-measure-${process.pid}`);
    console.log(`building ${bin}`);
    execFileSync("go", ["build", "-o", bin, "./cmd/tsagent"], {
        cwd: repoRoot,
        stdio: "inherit",
    });
    return bin;
}

function selectProbes(opts) {
    if (opts.probes.length > 0) {
        return opts.probes.map(name => {
            const probe = allProbes.find(p => p.name === name);
            if (!probe) {
                throw new Error(`unknown probe ${name}; run --list`);
            }
            return probe;
        });
    }
    return allProbes.filter(p => p.group === "small" || opts.heavy);
}

function runSuite({ label, bin, project, outDir, selected }) {
    fs.mkdirSync(outDir, { recursive: true });
    if (label) {
        console.log(`\n== ${label}: ${bin} ==`);
    }
    const rows = [];
    for (const probe of selected) {
        rows.push(runProbe({ probe, bin, project, outDir }));
    }
    writeSummary(outDir, rows);
    return rows;
}

function runProbe({ probe, bin, project, outDir }) {
    const stdoutPath = path.join(outDir, `${probe.name}.stdout`);
    const stderrPath = path.join(outDir, `${probe.name}.time`);
    const htmlPath = probe.html ? path.join(outDir, `${probe.name}.html`) : undefined;
    const args = [...probe.args, "--project", project];
    if (htmlPath) {
        args.push("--out", htmlPath);
    }

    console.log(`\n${probe.name}`);
    console.log(`${bin} ${args.join(" ")}`);
    const result = spawnSync(timeBin, ["-l", bin, ...args], {
        cwd: repoRoot,
        encoding: "utf8",
        maxBuffer: 1024 * 1024 * 100,
    });
    fs.writeFileSync(stdoutPath, result.stdout ?? "");
    fs.writeFileSync(stderrPath, result.stderr ?? "");
    if (result.error) {
        throw result.error;
    }

    const parsed = parseTime(result.stderr ?? "");
    const row = {
        probe: probe.name,
        status: result.status,
        realSeconds: parsed.realSeconds,
        maxRSSBytes: parsed.maxRSSBytes,
        peakFootprintBytes: parsed.peakFootprintBytes,
        stdoutPath,
        stderrPath,
        htmlPath,
    };
    console.log(formatRow(row));
    if (result.status !== 0) {
        console.log(`  exited ${result.status}; see ${stderrPath}`);
    }
    return row;
}

function parseTime(text) {
    return {
        realSeconds: numberMatch(text, /^\s*([\d.]+)\s+real/m),
        maxRSSBytes: integerMatch(text, /^\s*(\d+)\s+maximum resident set size/m),
        peakFootprintBytes: integerMatch(text, /^\s*(\d+)\s+peak memory footprint/m),
    };
}

function numberMatch(text, re) {
    const match = text.match(re);
    return match ? Number(match[1]) : undefined;
}

function integerMatch(text, re) {
    const match = text.match(re);
    return match ? Number.parseInt(match[1], 10) : undefined;
}

function writeSummary(outDir, rows) {
    fs.writeFileSync(path.join(outDir, "summary.json"), JSON.stringify(rows, undefined, 2) + "\n");
    const lines = [
        "| Probe | Exit | Wall | Max RSS | Peak footprint |",
        "| --- | ---: | ---: | ---: | ---: |",
        ...rows.map(row => `| ${row.probe} | ${row.status} | ${fmtSeconds(row.realSeconds)} | ${fmtGB(row.maxRSSBytes)} | ${fmtGB(row.peakFootprintBytes)} |`),
        "",
    ];
    fs.writeFileSync(path.join(outDir, "summary.md"), lines.join("\n"));
}

function writeComparison(outDir, baselineRows, candidateRows) {
    const candidateByProbe = new Map(candidateRows.map(row => [row.probe, row]));
    const comparisons = baselineRows.map(base => {
        const candidate = candidateByProbe.get(base.probe);
        return {
            probe: base.probe,
            baseline: base,
            candidate,
            realDeltaPct: pctDelta(base.realSeconds, candidate?.realSeconds),
            rssDeltaPct: pctDelta(base.maxRSSBytes, candidate?.maxRSSBytes),
            footprintDeltaPct: pctDelta(base.peakFootprintBytes, candidate?.peakFootprintBytes),
        };
    });

    fs.writeFileSync(path.join(outDir, "comparison.json"), JSON.stringify(comparisons, undefined, 2) + "\n");
    const lines = [
        "| Probe | Wall before | Wall after | Wall delta | RSS before | RSS after | RSS delta | Footprint before | Footprint after | Footprint delta |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
        ...comparisons.map(c => `| ${c.probe} | ${fmtSeconds(c.baseline.realSeconds)} | ${fmtSeconds(c.candidate?.realSeconds)} | ${fmtPct(c.realDeltaPct)} | ${fmtGB(c.baseline.maxRSSBytes)} | ${fmtGB(c.candidate?.maxRSSBytes)} | ${fmtPct(c.rssDeltaPct)} | ${fmtGB(c.baseline.peakFootprintBytes)} | ${fmtGB(c.candidate?.peakFootprintBytes)} | ${fmtPct(c.footprintDeltaPct)} |`),
        "",
    ];
    fs.writeFileSync(path.join(outDir, "comparison.md"), lines.join("\n"));
}

function formatRow(row) {
    return `  exit=${row.status} wall=${fmtSeconds(row.realSeconds)} rss=${fmtGB(row.maxRSSBytes)} footprint=${fmtGB(row.peakFootprintBytes)}`;
}

function fmtSeconds(value) {
    return value === undefined ? "n/a" : `${value.toFixed(2)}s`;
}

function fmtGB(value) {
    return value === undefined ? "n/a" : `${(value / 1_000_000_000).toFixed(2)} GB`;
}

function pctDelta(before, after) {
    if (before === undefined || after === undefined || before === 0) {
        return undefined;
    }
    return ((after - before) / before) * 100;
}

function fmtPct(value) {
    if (value === undefined) {
        return "n/a";
    }
    const sign = value > 0 ? "+" : "";
    return `${sign}${value.toFixed(1)}%`;
}
