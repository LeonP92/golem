; import ... from "source"
(import_statement source: (string (string_fragment) @import))

; require("source")
(call_expression
  function: (identifier) @_req (#eq? @_req "require")
  arguments: (arguments (string (string_fragment) @import)))

; export function Foo
(export_statement declaration: (function_declaration name: (identifier) @export_fn))

; export class Foo
(export_statement declaration: (class_declaration name: (identifier) @export_type))

; export const foo = ...
(export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @export_fn)))
