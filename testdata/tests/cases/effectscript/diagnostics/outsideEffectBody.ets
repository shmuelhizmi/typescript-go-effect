// constructs outside effect bodies are errors

declare const ea: any;

function plain() {
  x <- ea            // error TS18100: '<-' binds are only allowed inside an effect body.
  <- ea              // error TS18100
  raise new Error()  // error TS18101: 'raise' is only allowed inside an effect body.
}

// fork/par/race/defer keep their identifier meaning outside effect bodies
// (e.g. `par[0]` stays an element access); a dedicated 18120 requires the
// native-checking phase.
