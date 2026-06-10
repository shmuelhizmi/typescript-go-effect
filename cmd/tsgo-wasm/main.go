//go:build js && wasm

// Command tsgo-wasm is the browser playground entrypoint: it runs the tsgo
// LSP server and compiler against an in-memory file system, exposed to
// JavaScript as globalThis.tsgoWasm.
//
// JS surface:
//
//	init(manifestJSON: string, blob: Uint8Array): Promise<void>
//	  manifestJSON = {"files": {"/abs/path": [offset, length], ...}}; file
//	  bodies are concatenated UTF-8 in blob. Builds the in-memory workspace
//	  and starts the LSP server.
//	onLspMessage(cb: (bytes: Uint8Array) => void): void
//	  Registers the consumer of the server's outgoing LSP byte stream
//	  (Content-Length framed). Register before init.
//	lspWrite(bytes: Uint8Array): void
//	  Feeds client->server LSP bytes (Content-Length framed).
//	writeFile(path: string, content: string): string | null
//	  Updates a file in the in-memory FS; returns an error message or null.
//	readFile(path: string): string | null
//	  Reads a file from the in-memory FS ("/project/..." or "bundled:///...").
//	compile(): Promise<CompileResult>
//	  Compiles /project/tsconfig.json and resolves with emitted files plus
//	  config/syntactic diagnostics.
package main

import "syscall/js"

func main() {
	api := js.Global().Get("Object").New()
	api.Set("init", js.FuncOf(jsInit))
	api.Set("lspWrite", js.FuncOf(jsLspWrite))
	api.Set("onLspMessage", js.FuncOf(jsOnLspMessage))
	api.Set("writeFile", js.FuncOf(jsWriteFile))
	api.Set("readFile", js.FuncOf(jsReadFile))
	api.Set("compile", js.FuncOf(jsCompile))
	js.Global().Set("tsgoWasm", api)
	select {}
}
