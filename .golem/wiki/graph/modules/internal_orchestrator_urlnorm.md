# internal/orchestrator/urlnorm

The urlnorm module provides a single canonicalization function for git remote URLs. It lowercases the scheme and host, strips trailing ".git" suffixes and trailing slashes, and removes query strings and fragments so that two URLs pointing to the same repository always compare equal. The module exists to give the orchestrator a reliable identity key for remote repositories regardless of how the URL was originally typed or cloned.

## Functions

- Normalize
- TestNormalize

## Imports

net/url, strings, testing, github.com/leonp92/golem/internal/orchestrator/urlnorm
