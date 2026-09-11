# internal/slug

Slugification helper used to turn a ticket's human-entered `Title` into a
git-safe branch name component.

- `Slug(s string) string` — lowercases, collapses non-`[a-z0-9]` runs into a
  single `-`, trims leading/trailing `-`, truncates to 40 chars, and returns
  `"untitled"` for empty/unslugifiable input.
- `Branch(title, ticketID string) string` — returns
  `ticket/<slug(title)>-<ticketID[:8]>`, the working branch name computed
  once at ticket-creation time and threaded through claim/resume/worktree
  creation/crash-recovery instead of being re-derived as `ticket/<uuid>`.
