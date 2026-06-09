// raise + postfix catch arms (SPEC §5–6, TRANSPILATION §3–4)

import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}
class DbError extends Data.TaggedError("DbError")<{}> {}
class NetError extends Data.TaggedError("NetError")<{}> {}
class HttpError extends Data.TaggedError("HttpError")<{ status: number; cause?: unknown }> {}

declare const renderUser: (id: string) => any;
declare const render404: (id: string) => string;

effect raises(id: string) {
  if (id === "") raise new HttpError({ status: 400 })
  const v = id !== "x" ? id : raise new NotFound({ id })
  raise.die new Error("boom")
}

effect catches(id: string): string {
  html <- renderUser(id) catch {
    NotFound as e           >> render404(e.id)
    DbError | NetError as e >> raise new HttpError({ status: 503, cause: e })
    _ as e                  >> { <- Effect.logError(e); return "oops" }
  }
  return html
}

effect regionGuard(id: string): string {
  page <- effect {
    user <- renderUser(id)
    return String(user)
  } catch {
    NotFound >> "missing"
  }
  return page
}
