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
                  | BindDeclaration
                  | DiscardBindStatement
                  | RaiseStatement
                  | DeferStatement
                  | UsingBindStatement
                  | TypedTryStatement

BindDeclaration ::=
    ( 'const' | 'let' ) BindDeclaratorList ';'

BindDeclaratorList ::= BindDeclarator { ',' ( BindDeclarator | TS:VariableDeclarator ) }

BindDeclarator ::=
    ( BindingIdentifier | BindingPattern ) [ ':' Type ] '<-' AssignmentExpression

DiscardBindStatement ::= '<-' AssignmentExpression ';'

RaiseStatement ::= 'raise' [ '.' 'die' ] AssignmentExpression ';'
                                          // no LineTerminator after 'raise'

DeferStatement ::= 'defer' [ '(' BindingIdentifier ')' ] EffectBody

UsingBindStatement ::=
    'using' BindingIdentifier '<-' AssignmentExpression
    [ 'release' '(' ParameterList ')' EffectBody ] ';'

TypedTryStatement ::=
    'try' EffectBody { TypedCatchClause } [ CatchAllClause ] [ FinallyClause ]

TypedCatchClause ::= 'catch' '(' BindingIdentifier ':' Type ')' EffectBody
CatchAllClause   ::= 'catch' '(' BindingIdentifier ')' EffectBody
FinallyClause    ::= 'finally' EffectBody
```

Notes:
* `TypedTryStatement` requires at least one of: a `TypedCatchClause`,
  `CatchAllClause`, or `FinallyClause`. A classic single untyped `catch` with no
  type annotation parses as `TS:TryStatement` (standard semantics, warning 18111).
* `BindDeclaration` is distinguished from `TS:VariableStatement` by the `<-` after
  the (optionally typed) binding — one-token lookahead past the binding/type.

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
                    | TryExpression               // (effect body)

EffectFunctionExpression ::=
    'effect' '(' [ ParameterList ] ')' [ EffectReturnAnnotation ] EffectBody

EffectBlockExpression ::= 'effect' EffectBody

BindExpression ::= '(' '<-' AssignmentExpression ')'

ParExpression ::=
    'par' [ '(' AssignmentExpression ')' ]        // concurrency limit
    ( '[' ElementList ']' | ObjectLiteral )

RaceExpression ::= 'race' '[' ElementList ']'

TryExpression ::= TypedTryStatement               // in expression position

BinaryExpression ::= TS:BinaryExpression
                   | BinaryExpression '|>' BinaryExpression
                     // precedence: between '??' and Conditional; left-assoc
```

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
| `const x <- e` | BindDeclarator (the `<-` is impossible in TS here) |
| statement-initial `<- e` in `.etsx` | BindArrow: JSX requires ident/`>`/`/` after `<` |
| `(<- e)` vs parenthesized `<` | `<` cannot start an expression in `.ets` outside type assertions; in `.etsx` JSX needs an ident — `(<-` is unambiguous |
| `effect` identifier vs keyword | keyword iff followed (same line) by ident+`(` at decl position, or `(`/`{` at expr position |
| `raise`/`fork`/`par`/`race`/`join`/`defer` | keywords only at statement/unary head inside effect bodies |
| `x |> y` vs `x | (>...)` | `|>` is a single token; `|` followed by `>` cannot occur in valid TS expressions |
| `service`/`layer` | keyword iff followed by identifier at declaration position (same scheme as `type`) |
