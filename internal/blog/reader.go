package blog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// maxLine is the longest line, without its line ending, decoded whole.
	maxLine = 1 << 20
	// keepBytes is how much of each end of a longer line is kept. Both ends
	// and the marker fit the 64 KiB a forwarded entry may carry.
	keepBytes = 31 << 10
)

// ForEach calls fn for each line of r in order: with nil, nil for an empty
// line, otherwise with the entry or its decode error. terminated is false
// for a last line with no newline, which a writer may still be appending to.
//
// A line over maxLine bytes is never held whole. It becomes an entry with
// the fields written before its message and, as the message, the plain text
// of the message's start and end behind a "[line truncated: N bytes]"
// marker, so one huge line cannot fail a read. A long line that does not
// start as a JSON object is an error, as a short malformed line is; a part
// that cannot be decoded is kept as its raw bytes. An error from fn stops
// the read.
func ForEach(r io.Reader, fn func(e *Entry, err error, terminated bool) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line, tail []byte
	size := 0
	for {
		chunk, err := br.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull && err != io.EOF {
			return err
		}
		size += len(chunk)
		switch {
		case size <= maxLine+len("\r\n"):
			line = append(line, chunk...)
		case tail == nil:
			// First overflow: keep the head, start the tail.
			line = append(line, chunk...)
			tail = append([]byte{}, line[len(line)-2*keepBytes:]...)
			line = line[:keepBytes]
		default:
			tail = append(tail, chunk...)
			if len(tail) > 4*keepBytes {
				tail = tail[:copy(tail, tail[len(tail)-2*keepBytes:])]
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if size > 0 {
			e, derr := decodeLine(line, tail, size)
			if ferr := fn(e, derr, err == nil); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		line, tail, size = line[:0], nil, 0
	}
}

func decodeLine(line, tail []byte, size int) (*Entry, error) {
	if tail == nil {
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			return nil, nil
		}
		if len(line) <= maxLine {
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			return &e, nil
		}
		size = len(line)
		line, tail = line[:keepBytes], line[len(line)-keepBytes:]
	} else {
		n := len(tail)
		tail = bytes.TrimRight(tail, "\r\n")
		size -= n - len(tail)
		tail = tail[max(0, len(tail)-keepBytes):]
	}
	return truncated(line, tail, size)
}

// messageEnd matches the close of an entry's message: its quote, the
// optional fields the writer puts after it, and the closing brace.
var messageEnd = regexp.MustCompile(`"(?:,"(?:commit_sha|model)":"[^"\\]*")*\}$`)

// truncated rebuilds an entry from the head and tail of an over-long line.
// The fields written before the message (timestamp, role, type, target, id,
// in_reply_to) are recovered from head. commit_sha and model come after the
// message and are dropped; no reader of a log needs them from such an entry.
func truncated(head, tail []byte, size int) (*Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(head))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("log line of %d bytes is not a JSON object", size)
	}
	fields := map[string]json.RawMessage{}
	headText := string(head)
	for dec.More() {
		k, err := dec.Token()
		key, ok := k.(string)
		if err != nil || !ok {
			break
		}
		if key == "message" {
			rest := head[dec.InputOffset():]
			if i := bytes.IndexByte(rest, '"'); i >= 0 {
				headText = unquoteFragment(rest[i+1:], false)
			}
			break
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			break
		}
		fields[key] = v
	}
	var e Entry
	if b, err := json.Marshal(fields); err == nil {
		_ = json.Unmarshal(b, &e) // a mistyped field stays zero
	}
	tailText := string(tail)
	if loc := messageEnd.FindIndex(tail); loc != nil && !escaped(tail, loc[0]) {
		tailText = unquoteFragment(tail[:loc[0]], true)
	}
	e.Message = strings.ToValidUTF8(fmt.Sprintf("[line truncated: %d bytes; only its start and end are kept]\n%s\n[...]\n%s", size, headText, tailText), "\uFFFD")
	return &e, nil
}

// escaped reports whether b[i] follows an odd run of backslashes.
func escaped(b []byte, i int) bool {
	n := 0
	for i-n > 0 && b[i-n-1] == '\\' {
		n++
	}
	return n%2 == 1
}

// unquoteFragment decodes part of a JSON string body. The cut edge (the
// start when cutStart, else the end) may split a rune, an escape such as
// \u00e9, or a surrogate pair: the split rune's bytes are dropped, up to six
// bytes are dropped until it decodes, and a replacement character the split
// left within six bytes of the edge goes with what precedes or follows it.
// Raw bytes when it does not decode.
func unquoteFragment(f []byte, cutStart bool) string {
	if cutStart {
		for i := 0; i < utf8.UTFMax-1 && len(f) > 0 && !utf8.RuneStart(f[0]); i++ {
			f = f[1:]
		}
	} else if i := max(0, len(f)-(utf8.UTFMax-1)); len(f) > 0 {
		for j := len(f) - 1; j >= i; j-- {
			if utf8.RuneStart(f[j]) {
				if !utf8.FullRune(f[j:]) {
					f = f[:j]
				}
				break
			}
		}
	}
	const edge = 6
	for drop := 0; drop <= edge && drop <= len(f); drop++ {
		g := f[:len(f)-drop]
		if cutStart {
			g = f[drop:]
		}
		var s string
		if json.Unmarshal(append(append([]byte{'"'}, g...), '"'), &s) != nil {
			continue
		}
		if cutStart {
			if i := strings.IndexRune(s, utf8.RuneError); i >= 0 && i <= edge {
				s = s[i+len(string(utf8.RuneError)):]
			}
		} else if i := strings.LastIndex(s, string(utf8.RuneError)); i >= 0 && len(s)-i <= edge {
			s = s[:i]
		}
		return s
	}
	return string(f)
}
