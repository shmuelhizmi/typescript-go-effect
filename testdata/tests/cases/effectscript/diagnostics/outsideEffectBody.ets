// constructs outside effect bodies are errors
// (expected positions are line-level; exact columns are finalized when the
// suite is wired into the test runner)

declare const ea: any;

function plain() {
  x <- ea            // error TS18100: '<-' bind is only allowed inside an effect body.
  <- ea              // error TS18100
  raise new Error()  // error TS18101: 'raise' is only allowed inside an effect body.
  const f = fork ea  // error TS18120: 'fork' is only allowed inside an effect body.
  const p = par [ea] // error TS18120
  const c = ea catch { _ >> 1 } // error TS18113: catch arms outside effect body
}
