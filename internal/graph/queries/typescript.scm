; import ... from "source"
(import_statement source: (string (string_fragment) @import))

; export function Foo
(export_statement declaration: (function_declaration name: (identifier) @export_fn))

; export class Foo / export interface Foo
(export_statement declaration: (class_declaration name: (type_identifier) @export_type))
(export_statement declaration: (interface_declaration name: (type_identifier) @export_type))

; export type Foo = ...
(export_statement declaration: (type_alias_declaration name: (type_identifier) @export_type))
