# internal/blog

The blog module implements a ticket's append-only blackboard log — a structured JSONL file where every agent role records timestamped entries during a ticket's lifecycle. It defines the Entry type with its taxonomy of entry types (status, finding, blocker, question, answer, timeout, blocked tool call, and resolved), a mutex-guarded Writer that serialises concurrent appends safely, and a ReadAll reader that tolerates a missing log file as an empty state. The module exists to give all Golem agents a shared, corruption-free audit trail for a single ticket.

## Functions

- NewEntry
- TestNewEntrySetsFields
- NewWriter
- Append
- Close
- ReadAll
- TestWriterAppendAndReadAll
- TestWriterAppendIsConcurrencySafe
- TestReadAllMissingFile

## Types

- EntryType
- Entry
- Writer

## Imports

time, testing, bufio, encoding/json, os, sync, path/filepath
