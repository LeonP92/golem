# internal/blog

The blog module implements a ticket's append-only blackboard log — a structured JSONL file where every agent role records timestamped entries during a ticket's lifecycle. It defines the Entry type with its taxonomy of entry types (status, finding, blocker, question, answer, etc.), a mutex-guarded Writer that serialises concurrent appends safely, and a ReadAll reader that tolerates a missing log file as an empty state. The module exists to give all Golem agents a shared, corruption-free audit trail for a single ticket.

## Functions

- func NewEntry(role string, typ EntryType, message string) Entry — constructs an Entry with the current timestamp
- func NewWriter(path string) (*Writer, error) — opens or creates a JSONL log file for appending
- func (w *Writer) Append(e Entry) error — mutex-guarded append of one JSON line to the log
- func (w *Writer) Close() error — closes the underlying file
- func ReadAll(path string) ([]Entry, error) — reads and parses all entries from a log file; missing file returns empty slice

## Types

- Entry — one structured record in the ticket's append-only blackboard log
- EntryType — string enum discriminating the kind of log entry
- Writer — mutex-guarded, append-only writer for a ticket log file
