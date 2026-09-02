# internal/roles

The roles module bundles the default Golem agent role definitions (markdown files) as embedded assets and provides utilities to unpack them into a target repository's golem directory on first initialisation. It enforces a repo-ownership model where unpacked files are never overwritten on subsequent runs, allowing teams to customise their role definitions freely after the initial `golem init`.

## Functions

- func Unpack(golemDir string) ([]string, error) — writes embedded default role .md files into <golemDir>/roles/, skipping any that already exist; returns paths of newly written files

## Types

- Defaults — embedded filesystem (embed.FS) containing defaults/*.md role files
- RoleNames — ordered list of canonical role name strings
