// match value mode: multiple literal arms + guards + `_` default, bound in an effect

declare const classify: (n: number) => any;

effect grade(score: number): string {
  const tier = match (score) {
    0 | 1 | 2          >> "low"
    n if n >= 90       >> "excellent"
    n if n >= 50       >> "pass"
    _                  >> "fail"
  }
  detail <- classify(score)
  return `${tier}:${String(detail)}`
}
