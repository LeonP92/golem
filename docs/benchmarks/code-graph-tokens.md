# Code graph token benchmark (local, no LLM calls)

Question: does `golem graph build` cut "orientation" tokens by ~50% on a ~50-file repo and ~80% on larger ones, as the README says?

**Short answer: not reproduced.** In this proxy benchmark the graph beats a grep-and-read baseline only if that baseline wastes several file reads. Against a baseline that opens the right file first try, the graph cost more tokens in all three repos. The savings grow with repo size, matching the README's direction, but none reached 80%.

## What was measured

Task: "find where top-level Go symbol X is defined and read what it does". Per repo, a random sample (seed 7) of exported top-level funcs and types with a unique name was used. Tokens are counted with tiktoken `cl100k_base`.

- **Baseline A (optimistic)**: `git grep -w X` output (first 200 lines), then read the defining file in full. Assumes the first guess is right.
- **Baseline B (pessimistic)**: same, but the agent first reads up to 3 other files from the grep hits.
- **Graph arm**: grep `symbols.md` and `types.md` for X, then read the whole module file under `.golem/wiki/graph/modules/` for the matching package.

Graphs were built with `golem graph build`. No `claude` backend was available, so the LLM-written subsystem narratives are missing. They only live in the index, which this arm never reads, but a real graph has them.

## Results (mean tokens per lookup)

| Repo | Go files (non-test) | Symbols | Baseline A | Baseline B | Graph | Graph vs A | Graph vs B |
|---|---|---|---|---|---|---|---|
| gin-gonic/gin | 59 | 60 | 2,620 | 15,369 | 14,125 | -439.1% | 8.1% |
| LeonP92/golem | 135 | 200 | 2,366 | 8,267 | 4,074 | -72.2% | 50.7% |
| kubernetes/kubernetes | 10021 | 200 | 12,083 | 36,019 | 15,785 (17,540 vs B) | -30.6% | 51.3% |

Negative means the graph used more tokens. On kubernetes 6% of sampled symbols were missing from the graph. A miss is charged as the graph lookup plus the baseline cost, and that is included above. That is why kubernetes has two graph means: a miss falls back to baseline A in one comparison and to baseline B in the other.

Size of the graph artifacts a model would have to avoid reading whole: kubernetes `symbols.md` + `types.md` is about 1,332,503 tokens and `index.md` about 167,459. Both only work through grep.

## Reading

- Package-level module files are big in large packages, so one lookup often costs more than reading the single defining file.
- The graph helps when the agent would otherwise wander through grep hits. That is the realistic case on a huge repo, but this benchmark does not measure it.
- This is a locate-a-symbol proxy, not a real agent session. Real agents also use directory listings, search tools and the model's own prior knowledge. A proper test needs real model runs (paid) on fixed tickets, comparing tokens with and without the graph.

## Follow-ups

- The README no longer states "~50% / ~80%"; restate a number only once a model-run benchmark backs it.
- Module graph files could be split per file, or symbol lines could carry file paths and signatures, so a lookup does not need the whole module file.

## How it was run

On shallow clones, after `golem init` and `golem graph build`, with a throwaway Python script using `git grep` and tiktoken. 2026-10-13, golem main fdcc808.
