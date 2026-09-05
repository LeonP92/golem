; import foo / import foo.bar
(import_statement (dotted_name) @import)

; from foo import bar (captures the module, not the symbol)
(import_from_statement module_name: (dotted_name) @import)
(import_from_statement module_name: (relative_import) @import)

; Exported functions (no leading underscore = public by convention)
((function_definition name: (identifier) @export_fn)
 (#not-match? @export_fn "^_"))

; Exported classes
((class_definition name: (identifier) @export_type)
 (#not-match? @export_type "^_"))
