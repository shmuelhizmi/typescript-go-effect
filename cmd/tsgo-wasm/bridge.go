//go:build js && wasm

package main

import (
	"bytes"
	"fmt"
	"sync"
	"syscall/js"
)

// byteQueue is an unbounded byte buffer whose Read blocks until data arrives.
// Append never blocks, so it is safe to call from a js.FuncOf callback (which
// must not block: it would deadlock the single-threaded JS event loop).
type byteQueue struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
}

func newByteQueue() *byteQueue {
	q := &byteQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *byteQueue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.buf.Len() == 0 {
		q.cond.Wait()
	}
	return q.buf.Read(p)
}

func (q *byteQueue) Append(p []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.buf.Write(p)
	q.cond.Signal()
}

var (
	lspIn        = newByteQueue()
	onLspMessage = js.Null()
)

// jsWriter forwards each Write to the JS callback registered via
// onLspMessage. Called from the server's outgoing-queue goroutine; calling
// into JS from any goroutine is legal on js/wasm.
type jsWriter struct{}

func (jsWriter) Write(p []byte) (int, error) {
	if onLspMessage.IsNull() {
		return 0, fmt.Errorf("no onLspMessage callback registered")
	}
	u8 := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(u8, p)
	onLspMessage.Invoke(u8)
	return len(p), nil
}

// consoleWriter routes the server's stderr to console.error.
type consoleWriter struct{}

func (consoleWriter) Write(p []byte) (int, error) {
	js.Global().Get("console").Call("error", string(p))
	return len(p), nil
}

func jsLspWrite(this js.Value, args []js.Value) any {
	b := make([]byte, args[0].Get("length").Int())
	js.CopyBytesToGo(b, args[0])
	lspIn.Append(b)
	return nil
}

func jsOnLspMessage(this js.Value, args []js.Value) any {
	onLspMessage = args[0]
	return nil
}

// newPromise returns a JS Promise backed by fn running on its own goroutine.
// All exported functions that do real work go through here so the js.FuncOf
// callback itself returns immediately.
func newPromise(fn func() (any, error)) js.Value {
	executor := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer func() {
				if r := recover(); r != nil {
					reject.Invoke(js.ValueOf(fmt.Sprint(r)))
				}
			}()
			v, err := fn()
			if err != nil {
				reject.Invoke(js.ValueOf(err.Error()))
			} else {
				resolve.Invoke(js.ValueOf(v))
			}
		}()
		return nil
	})
	// The Promise constructor invokes the executor synchronously, so it can
	// be released as soon as New returns.
	p := js.Global().Get("Promise").New(executor)
	executor.Release()
	return p
}
