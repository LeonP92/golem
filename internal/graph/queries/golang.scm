; Imports
(import_spec path: (interpreted_string_literal) @import)

; Exported functions (capitalized first letter)
((function_declaration name: (identifier) @export_fn)
 (#match? @export_fn "^[A-Z]"))

; Exported method declarations on types
((method_declaration name: (field_identifier) @export_fn)
 (#match? @export_fn "^[A-Z]"))

; Exported types
((type_declaration (type_spec name: (type_identifier) @export_type))
 (#match? @export_type "^[A-Z]"))
