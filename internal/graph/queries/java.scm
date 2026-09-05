; import com.example.Foo
(import_declaration (scoped_identifier) @import)

; public class Foo / public interface Foo
(class_declaration
  (modifiers) @_m
  name: (identifier) @export_type
  (#match? @_m "public"))

; public void foo() / public static void foo()
(method_declaration
  (modifiers) @_mod
  name: (identifier) @export_fn
  (#match? @_mod "public"))
