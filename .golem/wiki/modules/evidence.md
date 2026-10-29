# evidence

`internal/evidence` — keeps secrets out of logs, prompts and persisted evidence
(defense in depth, not a sandbox).

- `Clean` redacts env-var secrets, credential assignments and private keys, and
  omits diff sections of sensitive files. Assignment redaction replaces values only,
  preserving quotes in structured output.
- `Writer` does the same line by line for streams, keeping sensitive-diff omission
  state until the next diff header. A private key block is
  redacted only when `-----BEGIN ... PRIVATE KEY-----` starts the line, and at
  most `maxKeyLines` lines, so a missing END line cannot hide the rest of a log.
- `SensitivePath` names live env/key files (`.env`, `*.env`, `.envrc`, `.netrc`,
  `.npmrc`, `.git-credentials`, ssh keys, `*.pem`, `*.key`); `.env.example`,
  `.sample` and `.template` stay readable.
