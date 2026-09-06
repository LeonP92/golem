# internal/soul

The soul module identifies moments where a human overrode a role's blocker with a different resolution, treating those divergences as candidate soul principles worth promoting. It extracts these candidates from a ticket's blog entries by pairing BLOCKER entries with human RESOLVED replies that differ from the original message, and provides a Promote function to persist accepted principles as markdown files in a designated soul directory.

## Functions

- ExtractCandidates
- Promote
- TestExtractCandidatesFindsDivergentResolution
- TestExtractCandidatesIgnoresNonDivergentActivity
- TestExtractCandidatesIgnoresResolutionMatchingOriginal
- TestExtractCandidatesIgnoresResolutionWithNoMatchingBlocker
- TestPromoteWritesFile
- TestPromoteCreatesSoulDirIfMissing

## Types

- Candidate

## Imports

os, path/filepath, github.com/leonp92/golem/internal/blog, testing
