# EffectScript Grammar

EBNF deltas over the TypeScript grammar (ECMA-262 + TS extensions as implemented in
this repository). Notation: `[x]` optional, `{x}` repetition, `|` alternation,
terminals quoted. Productions named `TS:*` refer to the unmodified host grammar.

A production marked **(effect body)** is only recognized while the parser is inside
an *EffectBody*; the parser tracks this with a context flag exactly like
`InsideJsxElement` / `AwaitContext`.

---

## 1. Tokens

```
BindArrow      ::= '<-'            // single token; see SPEC §2.1 for rescanning rules
PipeForward    ::= '|>'            // single token
```

Contextual keywords (never reserved): `effect raise service layer fork par race
defer release raises requires provide scoped join`.

## 2. Declarations

```
Declaration ::= TS:Declaration
              | EffectDeclaration
              | ServiceDeclaration
              | LayerDeclaration

EffectDeclaration ::=
    { Decorator } [ 'export' [ 'default' ] ]
    'effect' BindingIdentifier [ TypeParameters ]
    '(' [ ParameterList ] ')'
    [ EffectReturnAnnotation ]
    EffectBody

EffectReturnAnnotation ::=
    ':' Type                                    // must be assignable to Effect<any,any,any>
  | ':' Type 'raises' Type [ 'requires' Type ]  // sugar → Effect<A, E, R>
  | ':' Type 'requires' Type                    // sugar → Effect<A, never, R>

EffectBody ::= '{' { EffectStatement } '}'

ServiceDeclaration ::=
    [ 'export' ] 'service' BindingIdentifier [ TypeParameters ]
    '{' TS:TypeMemberList '}'

LayerDeclaration ::=
    [ 'export' ] [ 'scoped' ] 'layer' BindingIdentifier
    ':' Type                                    // the service tag
    [ 'provide' '[' AssignmentExpression { ',' AssignmentExpression } ']' ]
    ( EffectBody | '=' AssignmentExpression )
```

`effect` class members:

```
ClassElement ::= TS:ClassElement | EffectMethodDeclaration

EffectMethodDeclaration ::=
    { Decorator } [ 'static' ]
    'effect' PropertyName [ TypeParameters ]
    '(' [ ParameterList ] ')' [ EffectReturnAnnotation ]
    EffectBody
```

## 3. Statements (effect body)

```
EffectStatement ::= TS:Statement          // with the extensions below active
                  | BindStatement
                  | DiscardBindStatement
                  | RaiseStatement
                  | DeferStatement
                  | UsingBindStatement

BindStatement ::=
    BindTarget [ ':' Type ] '<-' AssignmentExpression ';'

BindTarget ::= BindingIdentifier | BindingPattern    // always immutable (const semantics)

DiscardBindStatement ::= '<-' AssignmentExpression ';'

RaiseStatement ::= 'raise' [ '.' 'die' ] AssignmentExpression ';'
                                          // no LineTerminator after 'raise'

DeferStatement ::= 'defer' [ '(' BindingIdentifier ')' ] EffectBody

UsingBindStatement ::=
    'using' BindingIdentifier '<-' AssignmentExpression
    [ 'release' '(' ParameterList ')' EffectBody ] ';'
```

Notes:
* `BindStatement` is recognized by scanning from statement start past a
  binding-identifier or balanced binding-pattern and optional `: Type` to a `<-`
  token (speculative parse, like arrow-function lookahead). On failure the
  statement re-parses as a plain `TS:Statement` (so `x < -y;` with whitespace, a
  labeled statement `x: y;`, and a block `{ ... }` all still parse normally).
* There is no `const`/`let`/`var` form of a bind, and no `finally`-style clause;
  finalization is `defer` and error handling is the postfix `catch` (§4).

## 4. Expressions

```
UnaryExpression ::= TS:UnaryExpression
                  | 'fork' UnaryExpression        // (effect body)
                  | 'join' UnaryExpression        // (effect body)
                  | RaiseExpression               // (effect body)

RaiseExpression ::= 'raise' [ '.' 'die' ] UnaryExpression

PrimaryExpression ::= TS:PrimaryExpression
                    | EffectFunctionExpression
                    | EffectBlockExpression
                    | BindExpression              // (effect body)
                    | ParExpression               // (effect body)
                    | RaceExpression              // (effect body)
                    | MatchExpression

EffectFunctionExpression ::=
    'effect' '(' [ ParameterList ] ')' [ EffectReturnAnnotation ] EffectBody

EffectBlockExpression ::= 'effect' EffectBody

BindExpression ::= '(' '<-' AssignmentExpression ')'

ParExpression ::=
    'par' [ '(' AssignmentExpression ')' ]        // concurrency limit
    ( '[' ElementList ']' | ObjectLiteral )

RaceExpression ::= 'race' '[' ElementList ']'

LeftHandSideExpression ::= TS:LeftHandSideExpression
                         | CatchExpression        // (effect body)

CatchExpression ::=                               // postfix, binds tighter than '|>'
    LeftHandSideExpression 'catch' '{' { CatchArm } '}'

CatchArm ::=
    TagReference { '|' TagReference } [ 'as' BindingIdentifier ] '>>' ArmBody
  | '_' [ 'as' BindingIdentifier ] '>>' ArmBody   // catch-all, must be last

TagReference ::= TS:TypeReference                 // class with string-literal '_tag'
ArmBody      ::= AssignmentExpression | EffectBody

BinaryExpression ::= TS:BinaryExpression
                   | BinaryExpression '|>' BinaryExpression
                     // precedence: between '??' and Conditional; left-assoc
```

## 4a. Match expressions

```
MatchExpression ::=
    'match' [ 'value' | 'tag' ] '(' Expression ')' '{' { MatchArm } '}'

MatchArm ::= MatchPattern { '|' MatchPattern } [ 'if' Expression ] '>>' ArmBody

MatchPattern ::=
    Literal                                       // number/string/boolean/null literal
  | '_' [ 'as' BindingIdentifier ]                // wildcard (must be in last arm)
  | BindingIdentifier                             // lowercase: binding (last arm or guarded)
  | TagReference [ 'as' BindingIdentifier ]       // Capitalized: tagged-class arm
  | ObjectMatchPattern
  | ArrayMatchPattern

ObjectMatchPattern ::= '{' [ ObjectMatchEntry { ',' ObjectMatchEntry } ] '}'
ObjectMatchEntry   ::= PropertyName ':' MatchPattern
                     | BindingIdentifier          // shorthand: binds the field

ArrayMatchPattern  ::= '[' [ MatchPattern { ',' MatchPattern } [ ',' '...' BindingIdentifier ] ] ']'
```

In `match tag (x)` form, every arm pattern must be a `TagReference` (or `_`).
Arms are newline- or `,`-separated; an arm's `ArmBody` expression extends as far as
ASI allows (same rules as expression statements).

## 5. Types

```
Type ::= TS:Type | EffectTypeSugar

EffectTypeSugar ::= Type 'raises' Type [ 'requires' Type ]
                  | Type 'requires' Type
```

Only recognized in `.ets`/`.etsx` files. `A raises E requires R` ≡
`Effect.Effect<A, E, R>`; omitted clauses are `never`. Binds tighter than `|`/`&`
on the left operand boundary; parenthesize unions: `(A | B) raises E`.

## 6. Disambiguation summary

| Source | Resolution |
| --- | --- |
| `a <- b` in expression context | relational `<` + unary `-` (BindArrow only at bind positions per §3/§4) |
| statement-initial `x <- e` | BindStatement via lookahead scan to `<-`; `x < -e` (spaced) stays relational |
| statement-initial `x: T <- e` | BindStatement, not a labeled statement (lookahead reaches `<-`) |
| statement-initial `{a} <- e` / `[a] <- e` | destructuring BindStatement, not block / array expression (lookahead) |
| statement-initial `<- e` in `.etsx` | BindArrow: JSX requires ident/`>`/`/` after `<` |
| `(<- e)` vs parenthesized `<` | `<` cannot start an expression in `.ets` outside type assertions; in `.etsx` JSX needs an ident — `(<-` is unambiguous |
| `effect` identifier vs keyword | keyword iff followed (same line) by ident+`(` at decl position, or `(`/`{` at expr position |
| `raise`/`fork`/`par`/`race`/`join`/`defer` | keywords only at statement/unary head inside effect bodies |
| postfix `catch {` | only after an expression inside effect bodies; `try`'s `catch` is always clause-position (never expression-postfix), so no conflict |
| `>>` in arms vs shift operator | inside `catch {`/`match {` braces the parser is in *arms context*; an arm's RHS expression may still use `>>` normally (the next arm starts at a pattern token after a line break/`,`) |
| `match (x) { ... }` vs call to `match` | speculative parse commits at `{` after the `)`; otherwise identifier call. `s.match(...)` unaffected |
| `x |> y` vs `x | (>...)` | `|>` is a single token; `|` followed by `>` cannot occur in valid TS expressions |
| `service`/`layer` | keyword iff followed by identifier at declaration position (same scheme as `type`) |
