// Thin JSON-RPC client over the worker's LSP MessagePort. The playground only
// needs requests, notifications, and server->client request auto-replies.

interface ResponseMessage {
  id: number
  result?: unknown
  error?: { code: number; message: string }
}

export class LspClient {
  #port: MessagePort
  #nextId = 1
  #pending = new Map<number, { resolve: (r: unknown) => void; reject: (e: Error) => void }>()
  #notificationListeners = new Map<string, Set<(params: unknown) => void>>()

  constructor(port: MessagePort) {
    this.#port = port
    port.onmessage = event => this.#handle(event.data as Record<string, unknown>)
  }

  #handle(msg: Record<string, unknown>) {
    if (msg.id !== undefined && msg.method === undefined) {
      const response = msg as unknown as ResponseMessage
      const pending = this.#pending.get(response.id)
      if (!pending) return
      this.#pending.delete(response.id)
      if (response.error) pending.reject(new Error(`${response.error.code}: ${response.error.message}`))
      else pending.resolve(response.result)
      return
    }
    if (msg.id !== undefined && msg.method !== undefined) {
      // Server -> client request. The playground accepts capability
      // (un)registrations and answers everything else with null too.
      this.#port.postMessage({ jsonrpc: "2.0", id: msg.id, result: null })
      return
    }
    const listeners = this.#notificationListeners.get(msg.method as string)
    if (listeners) {
      for (const listener of listeners) listener(msg.params)
    }
  }

  request<T = unknown>(method: string, params: unknown): Promise<T> {
    const id = this.#nextId++
    return new Promise<T>((resolve, reject) => {
      this.#pending.set(id, { resolve: resolve as (r: unknown) => void, reject })
      this.#port.postMessage({ jsonrpc: "2.0", id, method, params })
    })
  }

  notify(method: string, params: unknown): void {
    this.#port.postMessage({ jsonrpc: "2.0", method, params })
  }

  onNotification(method: string, listener: (params: unknown) => void): () => void {
    let listeners = this.#notificationListeners.get(method)
    if (!listeners) {
      listeners = new Set()
      this.#notificationListeners.set(method, listeners)
    }
    listeners.add(listener)
    return () => listeners.delete(listener)
  }
}
