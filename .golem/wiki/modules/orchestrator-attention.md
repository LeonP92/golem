# Bounded attention recovery

`internal/orchestrator/api/attention.go` keeps eligibility, attempt counts,
normalized failure fingerprints and dispatch leases on the existing ticket.
Only an explicit failure source opts a ticket in. Human parks, legacy rows,
exhausted PR/epic revision loops and non-repairable lane errors remain parked.

Attention claim/result requests require the owning shem, unchanged intake
approval, an active epic revision, a checkpoint and a matching unexpired
dispatch token. Feedback and phase change commit together. The ticket remains
assigned so the existing retry reconstruction preserves dirty work. Human
phase changes invalidate recovery tokens. Counts are durable across restart.

Stale claims return conflict; storage failures return server errors and roll
back recovery state and feedback together.
