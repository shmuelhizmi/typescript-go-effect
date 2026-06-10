// Headless end-to-end check against a running dev server: boots the page in
// the locally installed Chrome, waits for the compiler to come up, and
// reports console errors, the status bar, and the .JS pane.
//
//   node smoke/browser-check.mjs [url]
import { chromium } from "playwright-core";

const url = process.argv[2] ?? "http://localhost:5173";
const executablePath = process.env.CHROME_BIN
    ?? "/Applications/Google Chrome Dev.app/Contents/MacOS/Google Chrome Dev";
const browser = await chromium.launch({ executablePath, headless: true });
const page = await browser.newPage();

const consoleLines = [];
page.on("console", msg => {
    const loc = msg.location();
    consoleLines.push(`[${msg.type()}] ${msg.text()} (${loc.url}:${loc.lineNumber})`);
});
page.on("pageerror", error => consoleLines.push(`[pageerror] ${error.message}`));
page.on("requestfailed", request =>
    consoleLines.push(`[requestfailed] ${request.url()} -> ${request.failure()?.errorText}`));
page.on("response", response => {
    if (response.status() >= 400) consoleLines.push(`[http ${response.status()}] ${response.url()}`);
});

const t0 = Date.now();
await page.goto(url, { waitUntil: "domcontentloaded" });

// Wait for the compiler status to leave "booting" (wasm download + init).
let status = "";
try {
    await page.waitForFunction(
        () => !document.querySelector(".status")?.textContent?.includes("loading"),
        undefined,
        { timeout: 120_000 },
    );
    status = (await page.textContent(".status")) ?? "";
} catch {
    status = "TIMEOUT waiting for status change";
}
console.log(`status after ${((Date.now() - t0) / 1000).toFixed(1)}s: ${status}`);
const statusTitle = await page.getAttribute(".status", "title");
if (statusTitle) console.log(`status detail: ${statusTitle}`);

// Give the debounced compile a moment, then read the .JS pane.
await page.waitForTimeout(4000);
const jsPane = await page.textContent(".tab-content");
console.log(`\n.JS pane (first 400 chars):\n${(jsPane ?? "").slice(0, 400)}`);

// Try Run.
await page.click(".run-button");
await page.waitForTimeout(6000);
const runPane = await page.textContent(".tab-content");
console.log(`\nRun pane (first 400 chars):\n${(runPane ?? "").slice(0, 400)}`);

// Go to definition within main.ets: findUser usage -> its `effect` decl.
await page.evaluate(() => {
    const { editor, model } = window.__playground;
    const usage = model.findMatches('findUser("42")', false, false, true, null, false)[0];
    editor.setPosition({ lineNumber: usage.range.startLineNumber, column: usage.range.startColumn + 2 });
    editor.focus();
});
await page.keyboard.press("F12");
await page.waitForTimeout(1500);
const inFile = await page.evaluate(() => {
    const { editor, model } = window.__playground;
    return {
        uri: editor.getModel().uri.toString(),
        line: editor.getPosition().lineNumber,
        defLine: model.findMatches("effect findUser", false, false, true, null, false)[0]?.range.startLineNumber,
    };
});
console.log(`\ngo-to-definition (same file): landed ${inFile.uri}:${inFile.line}, expected line ${inFile.defLine}`);

// Go to definition across files: Effect.log -> effect d.ts (read-only model).
await page.evaluate(() => {
    const { editor, model } = window.__playground;
    editor.setModel(model);
    const usage = model.findMatches("Effect.log", false, false, true, null, false)[0];
    editor.setPosition({ lineNumber: usage.range.startLineNumber, column: usage.range.endColumn - 1 });
    editor.focus();
});
await page.keyboard.press("F12");
await page.waitForTimeout(2500);
const crossFile = await page.evaluate(() => {
    const { editor } = window.__playground;
    return { uri: editor.getModel().uri.toString() };
});
console.log(`go-to-definition (cross file): landed ${crossFile.uri}`);
const fileBar = await page.textContent(".file-bar").catch(() => null);
console.log(`file bar: ${fileBar}`);

console.log("\nconsole output:");
for (const line of consoleLines.slice(0, 40)) console.log("  " + line);

await browser.close();
