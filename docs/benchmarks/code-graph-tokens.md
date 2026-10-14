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

## Discovery and planning: Kubernetes

One ticket on Kubernetes `48f5417` (17,922 Go files): add a `kubelet_volume_mount_wait_failures_total` metric with a `reason` label. Planning it means finding `WaitForAttachAndMount`, its caller in `kubelet.go`, `VolumeAttachLimitExceededError` and the kubelet metrics `Register()`. Plan phase only, Opus, same spec, three runs per arm in fresh `golem-shem` containers.

| Arm | Plan correct | Cost | Time | Input tokens | Output tokens |
|---|---:|---:|---:|---:|---:|
| golem | 3/3 | $0.57 ($0.54–0.59) | 100 s (93–105) | 460k | 10.8k |
| plain `claude` | 3/3 | $0.77 ($0.54–0.82) | 135 s (92–153) | 610k | 15.4k |

Medians, with ranges. "Plan correct" means the plan names all five answer-key items: the metric in `metrics.go` and `Register()`, the `WaitForAttachAndMount` call site, `VolumeAttachLimitExceededError`, timeout detection, and a test file.

Golem found all four target symbols with one `grep` of `symbols.md`, then read only the listed ranges. Plain Claude searched the source tree. Both reached a correct plan every time; golem did it 26% cheaper and faster on the median, and its runs varied far less.

## Full ticket: gin

One ticket on gin `0f09c3a` (59 Go files): add MessagePack to `Context.Negotiate`, keeping the `nomsgpack` build tag working. Plan on Opus, implement on Sonnet, three runs per arm.

| Arm | Passed | Cost | Time |
|---|---:|---:|---:|
| golem | 3/3 | $0.57 | 117 s |
| plain `claude` | 3/3 | $0.58 | 135 s |

"Passed" is hidden acceptance tests plus `go test ./...` and `go vet -tags nomsgpack ./...`. On a repo this small, grep is already cheap and the graph adds little.
