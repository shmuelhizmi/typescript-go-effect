// Executes compiled playground JS in a sandboxed iframe. The import map
// points "effect" (and every subpath) at the pre-bundled ESM vendor build;
// console output and errors are forwarded to the parent via postMessage.

export interface ConsoleLine {
  level: "log" | "info" | "warn" | "error" | "debug"
  text: string
}

interface SandboxMessage {
  __playgroundConsole: true
  level: ConsoleLine["level"]
  text: string
}

let importMapPromise: Promise<Record<string, string>> | undefined

async function loadImportMap(): Promise<Record<string, string>> {
  importMapPromise ??= fetch("/vendor/effect/importmap.json")
    .then(r => {
      if (!r.ok) throw new Error(`import map fetch failed: ${r.status}`)
      return r.json() as Promise<{ imports: Record<string, string> }>
    })
    .then(map => {
      const absolute: Record<string, string> = {}
      for (const [specifier, target] of Object.entries(map.imports)) {
        absolute[specifier] = new URL(target, window.location.origin).href
      }
      return absolute
    })
  return importMapPromise
}

const BOOTSTRAP = `
(function () {
  const send = (level, text) => parent.postMessage({ __playgroundConsole: true, level, text }, "*");
  const fmt = v => {
    if (typeof v === "string") return v;
    if (v instanceof Error) return (v.stack || String(v));
    try { return JSON.stringify(v); } catch { return String(v); }
  };
  for (const level of ["log", "info", "warn", "error", "debug"]) {
    const original = console[level].bind(console);
    console[level] = (...args) => { original(...args); send(level, args.map(fmt).join(" ")); };
  }
  window.addEventListener("error", e => send("error", String(e.message)));
  window.addEventListener("unhandledrejection", e => send("error", "Unhandled rejection: " + fmt(e.reason)));
})();
`

/**
 * Replaces the previous run (if any) inside container with a fresh iframe
 * executing js. Returns a dispose function that also stops message routing.
 */
export async function runInSandbox(
  js: string,
  container: HTMLElement,
  onLine: (line: ConsoleLine) => void,
): Promise<() => void> {
  const imports = await loadImportMap()
  const iframe = document.createElement("iframe")
  iframe.setAttribute("sandbox", "allow-scripts")
  iframe.style.display = "none"

  const srcdoc = [
    "<!doctype html><html><head>",
    `<script type="importmap">${JSON.stringify({ imports })}</script>`,
    `<script>${BOOTSTRAP}</script>`,
    // Escape script terminators so user code can't break out of the tag.
    `<script type="module">${js.replaceAll("</script", "<\\/script")}</script>`,
    "</head><body></body></html>",
  ].join("\n")
  iframe.srcdoc = srcdoc

  const onMessage = (event: MessageEvent) => {
    if (event.source !== iframe.contentWindow) return
    const data = event.data as SandboxMessage
    if (data && data.__playgroundConsole === true) {
      onLine({ level: data.level, text: data.text })
    }
  }
  window.addEventListener("message", onMessage)
  container.replaceChildren(iframe)

  return () => {
    window.removeEventListener("message", onMessage)
    iframe.remove()
  }
}
