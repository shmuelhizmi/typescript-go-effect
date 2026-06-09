// misc diagnostics: plain try warning, scope check, decorator check, effect-block jumps

declare const ea: any;
declare const notACombinator: number;

effect tryWarning() {
  try {                  // warning TS18111: 'try' does not catch Effect failures
    x <- ea
    return x
  } catch (e) {
    return 0
  }
}

effect scopeNeeded() {
  using conn <- ea       // error TS18130 unless context provides a Scope
  return conn
}

@notACombinator          // error TS18140: decorator must be an Effect combinator
effect decorated() {
  return 1
}

effect jumps() {
  for (const i of [1, 2, 3]) {
    x <- effect {
      break              // error TS18112: 'break' cannot cross an effect block boundary
    } catch {
      _ >> 0
    }
  }
}
