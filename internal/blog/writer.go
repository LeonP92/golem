package blog

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// Writer is the single writer for a ticket's blackboard log. All agent
// processes append through this type (via the observer or `golem log
// emit`), never by writing the file directly, so concurrent appends
// never interleave or corrupt a line.
type Writer struct {
	mu   sync.Mutex
	file *os.File
}

func NewWriter(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{file: f}, nil
}

func (w *Writer) Append(e Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	_, err = w.file.Write(line)
	return err
}

func (w *Writer) Close() error {
	return w.file.Close()
}

// ReadAll returns every entry in the log at path, in append order. A
// missing file is treated as an empty log, not an error, since a
// not-yet-created ticket log is a normal state.
func ReadAll(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
