// using with a release body that binds, both one- and two-param release (SPEC §9)

declare const acquire: any;
declare const logClose: any;

effect xeConcReleaseBinds() {
  using a <- acquire() release (c) {
    closed <- c.close()
    <- logClose(closed)
  }
  using b <- acquire() release (res, exit) {
    flushed <- res.flush(exit)
    return flushed
  }
  return [a, b]
}
