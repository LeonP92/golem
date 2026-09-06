# internal/roles

The roles module bundles the default Golem agent role definitions (markdown files) as embedded assets and provides utilities to unpack them into a target repository's golem directory on first initialisation. It enforces a repo-ownership model where unpacked files are never overwritten on subsequent runs, allowing teams to customise their role definitions freely after the initial `golem init`.

## Functions

- Unpack
- TestUnpackWritesAllDefaultRoleFiles
- TestUnpackNeverOverwritesExistingRoleFile

## Imports

embed, os, path/filepath, testing
