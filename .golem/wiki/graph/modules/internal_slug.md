# internal/slug

The slug module converts free-text ticket titles into filesystem- and git-safe strings for use in branch names. It provides a Slug function that lowercases input, collapses non-alphanumeric runs into single hyphens, trims edge hyphens, truncates to 40 characters, and falls back to "untitled" for empty results, plus a Branch function that composes a full ticket branch name from a title and ticket ID using the pattern ticket/<slug>-<id[:8]>.

## Functions

- Slug
- Branch
- TestSlug
- TestBranch

## Imports

strings, testing
