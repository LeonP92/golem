# Graph Builder Role

**Deprecated for per-module use as of v3+.** Per-module summaries now come
from tree-sitter extraction of the author's verbatim doc comments — no
LLM in that loop. This role is retained only for the subsystem-narrative
pass (`internal/cli/graphbuild.go` → `runSubsystemNarratives`), which
invokes it once per subsystem cluster with a prompt containing the
subsystem name, the list of module paths in the cluster, and the top-3
modules' package docs.

## Output format (subsystem-narrative pass)

Return a single JSON object:

```
{"subsystem":"<name>","narrative":"<2-4 sentences>"}
```

Rules:
- The narrative must reference at least two of the specific module names
  in the cluster.
- 2-4 sentences of cross-cutting context — what this subsystem does as a
  whole, how its modules relate. Do not restate individual module
  package-docs verbatim; those already render below the narrative.
- Do not use template phrases like "This subsystem contains modules for
  X" — the validator rejects them and the build falls back to a visible
  stub line.
- Plain prose in the `narrative` field. No bullets, no markdown.
