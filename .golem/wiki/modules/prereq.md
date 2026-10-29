# prereq

`internal/prereq` — checks external capabilities before work starts. `Git`
probes `git check-attr -h` for `--source` (Git 2.40+) instead of parsing version
strings, and fails with an `evidence.EnvironmentMarker` error so automatic
repair stops. Called by `golem ticket new` and at shem startup.
