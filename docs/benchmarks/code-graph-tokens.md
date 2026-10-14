# Code graph benchmark

`symbols.md` and `types.md` list every export with its file and declaration range:

```
- `binding` (`binding/form_mapping.go`) [L182-L186] — BindUnmarshaler
```

An agent that looks a symbol up reads those 5 lines instead of the 550-line file.

## Discovery (no model calls)

**Range reads.** Tokens to find a symbol and read its definition: whole defining file vs. the indexed range. 200 random indexed names per repo, `cl100k_base`, lookup output counted in both.

| Repo | Whole file | Range | Reduction |
|---|---:|---:|---:|
| gin | 12,928 | 172 | 98.7% |
| Django | 4,552 | 453 | 90.1% |
| VS Code | 7,454 | 476 | 93.6% |
| Kubernetes | 12,510 | 643 | 94.9% |

**Lookup speed.** Finding a symbol's defining files: source search vs. graph search, warm cache, median of 3 rounds.

| Scope | Files | Source | Graph | Paths found |
|---|---:|---:|---:|---:|
| Django `django/` | 907 | 8.11 ms | 1.87 ms | 143/143 |
| VS Code `src/` | 10,410 | 82.09 ms | 4.48 ms | 97/99 |

**Keeping it current.** `graph update` after a one-file commit: gin 0.19 s, Django 0.08 s, VS Code 0.84 s, Kubernetes 0.76 s (narrative calls stubbed).

Example (Kubernetes `GetVolumesToMount`): whole file 7,059 tokens, range `L586-L616` 375 tokens.

These show what a lookup costs when an agent uses the index. The baseline reads whole files; an agent that greps and reads a slice pays less than that. C is not indexed.

## Agent run (gin)

One ticket on gin `0f09c3a` (59 Go files): add MessagePack to `Context.Negotiate`, keeping the `nomsgpack` build tag working. Three runs per arm in fresh `golem-shem` containers, same spec, plan on Opus, implement on Sonnet. Medians:

| Arm | Passed | Cost | Time | Input tokens | Output tokens |
|---|---:|---:|---:|---:|---:|
| golem (graph with ranges) | 3/3 | $0.57 | 117 s | 641k | 12.3k |
| plain `claude` | 3/3 | $0.58 | 135 s | 521k | 16.4k |

Plan phase alone: golem $0.30 / 55 s, plain $0.39 / 78 s (23% cheaper, 29% faster). Both start from the same ~17k context. Golem used 225k input vs 194k only because it made one more request (8 vs 7), and every request resends the whole context. It wrote shorter plans (6.0k vs 8.1k Opus output tokens), which is where the saving comes from.

"Passed" is hidden acceptance tests plus `go test ./...` and `go vet -tags nomsgpack ./...`. Golem's validation, observer and review gates were not run.

The agents never opened `symbols.md` or `types.md`; on 59 files they found the code with `grep -n`. So this run compares golem with plain Claude, not ranges with no ranges.
