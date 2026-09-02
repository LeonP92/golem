# internal/soul

The soul module identifies moments where a human overrode a role's blocker with a different resolution, treating those divergences as candidate soul principles worth promoting. It extracts these candidates from a ticket's blog entries by pairing BLOCKER entries with human RESOLVED replies that differ from the original message, and provides a Promote function to persist accepted principles as markdown files in a designated soul directory.

## Functions

- func ExtractCandidates(entries []blog.Entry) []Candidate — scans blog entries and returns candidates where a human resolved a blocker differently than the role suggested
- func Promote(soulDir, filename, content string) error — writes a principle file into the soul directory, creating the directory if needed

## Types

- Candidate — pairs a blog entry (the human resolution) with the extracted suggestion string

## Imports

github.com/leonpham/golem/internal/blog
