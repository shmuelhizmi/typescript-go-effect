// raises A | B declaration raising two distinct tagged errors, each caught by tag
// (SPEC §5 raises, §6 catch matching)

tagged error NotFound {
  id: string
}

tagged error Timeout {
  ms: number
}

declare const lookupRow: (id: string) => any;

// the producer can raise either tagged error depending on input
effect load(id: string): string raises NotFound | Timeout {
  if (id === "") raise new NotFound({ id })
  row <- lookupRow(id)
  if (row === undefined) raise new Timeout({ ms: 5000 })
  return String(row)
}

// the consumer catches each tag in its own arm with an `as e` binding
effect loadOrDefault(id: string): string {
  value <- load(id) catch {
    NotFound as e >> `missing:${e.id}`
    Timeout  as e >> `slow:${e.ms}`
  }
  return value
}
