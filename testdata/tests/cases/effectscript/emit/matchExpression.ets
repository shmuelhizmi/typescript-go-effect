// match expressions: value mode + tag mode (SPEC §11, TRANSPILATION §10)

import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}
class DbError extends Data.TaggedError("DbError")<{}> {}
class NetError extends Data.TaggedError("NetError")<{}> {}

declare const res: { status: number; body?: string };
declare const error: NotFound | DbError | NetError;

const label = match (res) {
  { status: 200, body }      >> body
  { status: 301 | 302 }      >> "redirect"
  { status: s } if s >= 500  >> `server error ${s}`
  _                          >> "unknown"
};

const sameAsAbove = match value (res) {
  { status: 200 } >> "ok"
  _               >> "other"
};

const msg = match tag (error) {
  NotFound as e      >> `missing ${e.id}`
  DbError | NetError >> "infra down"
};

declare const fetchFallback: (id: string) => any;

effect effectful(id: string) {
  const out = match tag (error) {
    NotFound as e >> { v <- fetchFallback(e.id); return v }
    _             >> "n/a"
  }
  return out
}
