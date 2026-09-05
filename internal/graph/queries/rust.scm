; use crate::foo or use external_crate::bar
(use_declaration argument: (_) @import)

; pub fn foo (exported functions)
(function_item
  (visibility_modifier) @_vis (#eq? @_vis "pub")
  name: (identifier) @export_fn)

; pub struct Foo / pub enum Foo / pub type Foo
(struct_item
  (visibility_modifier) @_vis (#eq? @_vis "pub")
  name: (type_identifier) @export_type)
(enum_item
  (visibility_modifier) @_vis (#eq? @_vis "pub")
  name: (type_identifier) @export_type)
(type_item
  (visibility_modifier) @_vis (#eq? @_vis "pub")
  name: (type_identifier) @export_type)
