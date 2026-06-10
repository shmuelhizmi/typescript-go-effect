//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"syscall/js"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/lsp"
	"github.com/microsoft/typescript-go/internal/playground"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

const workspaceRoot = "/project"

var (
	fsMu  sync.Mutex
	memFS vfs.FS
)

func currentFS() vfs.FS {
	fsMu.Lock()
	defer fsMu.Unlock()
	return memFS
}

func jsInit(this js.Value, args []js.Value) any {
	manifestJSON := args[0].String()
	blobJS := args[1]
	blob := make([]byte, blobJS.Get("length").Int())
	js.CopyBytesToGo(blob, blobJS)

	return newPromise(func() (any, error) {
		var manifest struct {
			Files map[string][2]int `json:"files"`
		}
		if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
			return nil, fmt.Errorf("invalid manifest: %w", err)
		}
		files := make(map[string]string, len(manifest.Files))
		for path, span := range manifest.Files {
			offset, length := span[0], span[1]
			if offset < 0 || length < 0 || offset+length > len(blob) {
				return nil, fmt.Errorf("file %q is out of blob range", path)
			}
			files[tspath.NormalizePath(path)] = string(blob[offset : offset+length])
		}

		fs := bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
		fsMu.Lock()
		memFS = fs
		fsMu.Unlock()

		server := lsp.NewServer(&lsp.ServerOptions{
			In:  lsp.ToReader(lspIn),
			Out: lsp.ToWriter(jsWriter{}),
			Err: consoleWriter{},
			Cwd: workspaceRoot,
			FS:  fs,
			// TypingsLocation "" + NpmInstall nil disable ATA
			// (internal/project/session.go); SetParentProcessID nil disables
			// the parent-process watchdog.
			DefaultLibraryPath: bundled.LibPath(),
		})
		go func() {
			if err := server.Run(context.Background()); err != nil {
				js.Global().Get("console").Call("error", "tsgo lsp server exited: "+err.Error())
			}
		}()
		return js.Undefined(), nil
	})
}

func jsWriteFile(this js.Value, args []js.Value) any {
	fs := currentFS()
	if fs == nil {
		return "tsgoWasm.init must be called first"
	}
	path := tspath.NormalizePath(args[0].String())
	if err := fs.WriteFile(path, args[1].String()); err != nil {
		return err.Error()
	}
	return nil
}

func jsCompile(this js.Value, args []js.Value) any {
	return newPromise(func() (any, error) {
		fs := currentFS()
		if fs == nil {
			return nil, fmt.Errorf("tsgoWasm.init must be called first")
		}
		result := playground.Compile(
			context.Background(),
			fs,
			workspaceRoot,
			workspaceRoot+"/tsconfig.json",
			bundled.LibPath(),
			false, /*checkSemantics*/
		)
		data, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return js.Global().Get("JSON").Call("parse", string(data)), nil
	})
}
