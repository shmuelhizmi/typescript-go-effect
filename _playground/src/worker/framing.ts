// LSP base-protocol framing: the Go server speaks Content-Length framed
// bytes; the MessagePort to the main thread carries parsed JSON messages.

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Encode one JSON-RPC message as LSP bytes. */
export function frame(msg: unknown): Uint8Array {
    const body = encoder.encode(JSON.stringify(msg));
    const head = encoder.encode(`Content-Length: ${body.length}\r\n\r\n`);
    const out = new Uint8Array(head.length + body.length);
    out.set(head);
    out.set(body, head.length);
    return out;
}

/** Incremental decoder: bytes (any chunking) -> JSON-RPC messages. */
export class FrameDecoder {
    #buffer = new Uint8Array(0);

    push(chunk: Uint8Array, onMessage: (msg: unknown) => void): void {
        const merged = new Uint8Array(this.#buffer.length + chunk.length);
        merged.set(this.#buffer);
        merged.set(chunk, this.#buffer.length);
        this.#buffer = merged;
        for (;;) {
            const headerEnd = indexOfSeq(this.#buffer, HEADER_TERMINATOR);
            if (headerEnd < 0) return;
            const header = decoder.decode(this.#buffer.subarray(0, headerEnd));
            const lengthMatch = /Content-Length: *(\d+)/i.exec(header);
            if (!lengthMatch) throw new Error(`LSP frame without Content-Length: ${header}`);
            const length = Number(lengthMatch[1]);
            const bodyStart = headerEnd + HEADER_TERMINATOR.length;
            if (this.#buffer.length < bodyStart + length) return;
            const body = decoder.decode(this.#buffer.subarray(bodyStart, bodyStart + length));
            this.#buffer = this.#buffer.slice(bodyStart + length);
            onMessage(JSON.parse(body));
        }
    }
}

const HEADER_TERMINATOR = [13, 10, 13, 10]; // \r\n\r\n

function indexOfSeq(buffer: Uint8Array, seq: ArrayLike<number>): number {
    outer: for (let i = 0; i + seq.length <= buffer.length; i++) {
        for (let j = 0; j < seq.length; j++) {
            if (buffer[i + j] !== seq[j]) continue outer;
        }
        return i;
    }
    return -1;
}
