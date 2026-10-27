# gate

`internal/gate` — runs `gate.environment_checks` then `gate.commands` with `sh -c`
and stops at the first failure. A failing environment check, exit 126 or 127, or a
process that could not start is an `environment` failure (output starts with
`evidence.EnvironmentMarker`); anything else is `code`. Exit 127 is not told apart
from a missing project script. Output is redacted with `evidence.Clean`.
