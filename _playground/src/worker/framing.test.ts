import { describe, expect, it } from "vitest";
import { FrameDecoder, frame } from "./framing";

function collect(decoder: FrameDecoder, chunks: Uint8Array[]): unknown[] {
    const messages: unknown[] = [];
    for (const chunk of chunks) decoder.push(chunk, m => messages.push(m));
    return messages;
}

describe("framing", () => {
    it("round-trips a message", () => {
        const msg = { jsonrpc: "2.0", id: 1, method: "x", params: { a: [1, 2] } };
        expect(collect(new FrameDecoder(), [frame(msg)])).toEqual([msg]);
    });

    it("decodes a frame split across arbitrary chunk boundaries", () => {
        const msg = { jsonrpc: "2.0", method: "note", params: { text: "héllo  " } };
        const bytes = frame(msg);
        for (let split = 1; split < bytes.length; split += 7) {
            const decoder = new FrameDecoder();
            const messages = collect(decoder, [bytes.subarray(0, split), bytes.subarray(split)]);
            expect(messages).toEqual([msg]);
        }
    });

    it("decodes multiple messages from one chunk", () => {
        const msgs = [{ id: 1 }, { id: 2 }, { id: 3 }];
        const joined = msgs.map(m => frame(m)).reduce((a, b) => {
            const out = new Uint8Array(a.length + b.length);
            out.set(a);
            out.set(b, a.length);
            return out;
        });
        expect(collect(new FrameDecoder(), [joined])).toEqual(msgs);
    });

    it("handles multi-byte UTF-8 lengths", () => {
        const msg = { text: "🚀".repeat(100) };
        expect(collect(new FrameDecoder(), [frame(msg)])).toEqual([msg]);
    });
});
