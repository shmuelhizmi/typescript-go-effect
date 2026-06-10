; Minimal highlights reusing the tree-sitter-typescript grammar.
; EffectScript's contextual keywords are not new grammar nodes, so they
; highlight as identifiers; the language server supplies semantics.

(identifier) @variable
(type_identifier) @type
(predefined_type) @type.builtin
(property_identifier) @property

(call_expression
  function: (identifier) @function)

(comment) @comment
(string) @string
(template_string) @string
(number) @number

[ "(" ")" "[" "]" "{" "}" ] @punctuation.bracket
[ ";" "." "," ":" ] @punctuation.delimiter

[
  "as" "async" "await" "break" "case" "catch" "class" "const" "continue"
  "default" "delete" "do" "else" "export" "extends" "finally" "for" "from"
  "function" "get" "if" "import" "in" "instanceof" "let" "new" "of" "return"
  "set" "static" "switch" "throw" "try" "type" "typeof" "var" "void" "while"
  "yield"
] @keyword

[ "true" "false" "null" "undefined" ] @constant.builtin
