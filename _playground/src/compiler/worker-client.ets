// Singleton owner of the compiler Web Worker: spawns tsgo.wasm once per page,
// exposes the LSP MessagePort and a promise-based compile call.
import type { CompileResult, WorkerRequest, WorkerResponse } from "./protocol"

export type WorkerStatus = "booting" | "ready" | "failed"

class WorkerClient {
  readonly lspPort: MessagePort
  readonly ready: Promise<void>
  #worker: Worker
  #nextId = 1
  #pending = new Map<number, { resolve: (r: CompileResult) => void; reject: (e: Error) => void }>()
  #pendingReads = new Map<number, (content: string | null) => void>()
  #status: WorkerStatus = "booting"
  #statusListeners = new Set<(s: WorkerStatus, detail?: string) => void>()

  constructor(initialSource: string) {
    this.#worker = new Worker(new URL("../worker/compiler.worker.ts", import.meta.url), {
      type: "module",
    })
    const channel = new MessageChannel()
    this.lspPort = channel.port1

    let resolveReady!: () => void
    let rejectReady!: (e: Error) => void
    this.ready = new Promise<void>((resolve, reject) => {
      resolveReady = resolve
      rejectReady = reject
    })

    this.#worker.onmessage = (event: MessageEvent<WorkerResponse>) => {
      const msg = event.data
      switch (msg.kind) {
        case "ready":
          this.#setStatus("ready")
          resolveReady()
          break
        case "fatal":
          this.#setStatus("failed", msg.message)
          rejectReady(new Error(msg.message))
          break
        case "compile-result": {
          const pending = this.#pending.get(msg.id)
          if (!pending) break
          this.#pending.delete(msg.id)
          if (msg.result) pending.resolve(msg.result)
          else pending.reject(new Error(msg.error ?? "compile failed"))
          break
        }
        case "file-content": {
          const resolve = this.#pendingReads.get(msg.id)
          if (!resolve) break
          this.#pendingReads.delete(msg.id)
          resolve(msg.content)
          break
        }
      }
    }

    const init: WorkerRequest = { kind: "init", source: initialSource }
    this.#worker.postMessage(init, [channel.port2])
  }

  get status(): WorkerStatus {
    return this.#status
  }

  onStatus(listener: (s: WorkerStatus, detail?: string) => void): () => void {
    this.#statusListeners.add(listener)
    listener(this.#status)
    return () => this.#statusListeners.delete(listener)
  }

  #setStatus(status: WorkerStatus, detail?: string) {
    this.#status = status
    for (const listener of this.#statusListeners) listener(status, detail)
  }

  async compilePromise(source: string): Promise<CompileResult> {
    // The first compile usually arrives while the ~50MB wasm is still
    // booting; hold requests until the worker reports ready.
    await this.ready
    const id = this.#nextId++
    const request: WorkerRequest = { kind: "compile", id, source }
    return new Promise<CompileResult>((resolve, reject) => {
      this.#pending.set(id, { resolve, reject })
      this.#worker.postMessage(request)
    })
  }

  /** Reads a file from the compiler's in-memory FS (sources, effect d.ts, bundled libs). */
  async readFile(path: string): Promise<string | null> {
    await this.ready
    const id = this.#nextId++
    const request: WorkerRequest = { kind: "read-file", id, path }
    return new Promise<string | null>(resolve => {
      this.#pendingReads.set(id, resolve)
      this.#worker.postMessage(request)
    })
  }
}

let client: WorkerClient | undefined

/** Spawns the worker on first use; initialSource only matters for that call. */
export function getWorkerClient(initialSource: string): WorkerClient {
  client ??= new WorkerClient(initialSource)
  return client
}
