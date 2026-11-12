package blog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func entryLine(t *testing.T, e Entry) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An entry over the line limit is kept as a truncated entry instead of
// failing the whole read: its fields and both ends of its message survive,
// decoded back to plain text.
func TestReadAllTruncatesOverlongLine(t *testing.T) {
	for name, body := range map[string]string{
		"plain":    strings.Repeat("gate output\n", 1<<20/12),
		"minified": `{"results":[` + strings.Repeat(`{"ok":true,"name":"x"},`, 1<<20/23) + `{"ok":false}]}`,
		"escapes":  strings.Repeat("\"q\" \\ \t é\n", 1<<17),
		"binary":   strings.Repeat("\x00\x01\x1f\x7f", 1<<18),
		"utf8":     strings.Repeat("日本語", 1<<19),
	} {
		t.Run(name, func(t *testing.T) {
			huge := NewEntry("gate", TypeBlocker, "START \"here\"\n"+body+"\nFINAL: TestFoo \"failed\"")
			huge.ID = "g1"
			huge.CommitSHA = "abc123"
			huge.Model = "some/model"
			path := writeLog(t, entryLine(t, NewEntry("developer", TypeStatus, "before"))+"\n"+
				entryLine(t, huge)+"\r\n"+
				entryLine(t, NewEntry("reviewer", TypeFinding, "after"))) // no trailing newline
			entries, err := ReadAll(path)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if len(entries) != 3 || entries[0].Message != "before" || entries[2].Message != "after" {
				t.Fatalf("neighbours lost: %d entries", len(entries))
			}
			e := entries[1]
			if e.Role != "gate" || e.Type != TypeBlocker || e.ID != "g1" || e.Timestamp.IsZero() {
				t.Errorf("fields lost: %+v", Entry{Role: e.Role, Type: e.Type, ID: e.ID, Timestamp: e.Timestamp})
			}
			if !strings.HasPrefix(e.Message, "[line truncated: ") || !strings.Contains(e.Message, "\nSTART \"here\"\n") ||
				!strings.HasSuffix(e.Message, "\nFINAL: TestFoo \"failed\"") {
				t.Errorf("not plain text: head %q tail %q", e.Message[:120], e.Message[len(e.Message)-80:])
			}
			if strings.Contains(e.Message, `\"`) || strings.Contains(e.Message, `abc123`) {
				t.Errorf("JSON syntax left in message: %q", e.Message[len(e.Message)-80:])
			}
			if len(e.Message) > 2*keepBytes+256 || !utf8.ValidString(e.Message) {
				t.Errorf("message %d bytes, valid utf8 %v", len(e.Message), utf8.ValidString(e.Message))
			}
		})
	}
}

// sizedLine is a log line of exactly n bytes, not counting its newline.
func sizedLine(t *testing.T, n int) string {
	t.Helper()
	prefix := entryLine(t, Entry{Role: "r", Type: TypeStatus, Message: "S"})
	pad := n - len(prefix) - len("END")
	return strings.Replace(prefix, `"S"`, `"S`+strings.Repeat("x", pad)+`END"`, 1)
}

func TestReadAllLineLimitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		truncated bool
	}{
		{"under", maxLine - 1, false},
		{"exact", maxLine, false},
		{"over by one", maxLine + 1, true},
		{"over by a chunk", maxLine + 65536, true},
		{"tail compacted", maxLine + 256<<10, true},
		{"many compactions", 3 * maxLine, true},
	} {
		for _, eol := range []string{"\n", "\r\n", ""} {
			t.Run(tc.name+" "+strings.ReplaceAll(strings.ReplaceAll(eol, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
				line := sizedLine(t, tc.size)
				if len(line) != tc.size {
					t.Fatalf("fixture %d bytes", len(line))
				}
				content, want := line, 1 // no newline: the last line
				if eol != "" {
					content, want = line+eol+entryLine(t, NewEntry("b", TypeStatus, "next"))+"\n", 2
				}
				entries, err := ReadAll(writeLog(t, content))
				if err != nil || len(entries) != want || want == 2 && entries[1].Message != "next" {
					t.Fatalf("got %d entries, err %v", len(entries), err)
				}
				m := entries[0].Message
				if got := strings.HasPrefix(m, "[line truncated: "); got != tc.truncated {
					t.Fatalf("truncated %v, want %v", got, tc.truncated)
				}
				if !strings.HasSuffix(m, "xEND") || entries[0].Role != "r" {
					t.Fatalf("end or role lost: %q", m[len(m)-20:])
				}
				if tc.truncated && (!strings.Contains(m, "\nSxxx") || len(m) > 2*keepBytes+256) {
					t.Fatalf("head lost or %d bytes", len(m))
				}
			})
		}
	}
}

// Normal logs read exactly as before.
func TestReadAllNormalLinesUnchanged(t *testing.T) {
	msg := strings.Repeat("x", maxLine-200)
	path := writeLog(t, entryLine(t, NewEntry("a", TypeStatus, msg))+"\r\n\n"+entryLine(t, NewEntry("b", TypeStatus, "y"))+"\n")
	entries, err := ReadAll(path)
	if err != nil || len(entries) != 2 || entries[0].Message != msg || entries[1].Message != "y" {
		t.Fatalf("got %d entries, err %v", len(entries), err)
	}
}

// A line that is not a log entry fails the read, long or short.
func TestReadAllRejectsMalformedLines(t *testing.T) {
	for _, content := range []string{"not json\n", strings.Repeat("not json ", maxLine/4) + "\n"} {
		if _, err := ReadAll(writeLog(t, content)); err == nil {
			t.Fatalf("malformed %d-byte line accepted", len(content))
		}
	}
}

// ForEach says which line ended without a newline, so a tailer can wait for
// the rest of it.
func TestForEachReportsUnterminatedLine(t *testing.T) {
	var ends []bool
	err := ForEach(strings.NewReader(entryLine(t, NewEntry("a", TypeStatus, "x"))+"\n"+sizedLine(t, maxLine+10)), func(_ *Entry, err error, terminated bool) error {
		ends = append(ends, terminated)
		return err
	})
	if err != nil || len(ends) != 2 || !ends[0] || ends[1] {
		t.Fatalf("terminated %v, err %v", ends, err)
	}
}

// The cut edge of a fragment may split an escape, a rune or a surrogate
// pair; what is left of the split character is dropped, nothing else.
func TestUnquoteFragmentCutEdges(t *testing.T) {
	for _, tc := range []struct {
		name     string
		frag     string
		cutStart bool
		want     string
	}{
		{"head inside é", "abc\xc3", false, "abc"},
		{"tail inside é", "\xa9def", true, "def"},
		{"head inside 😀 bytes", "abc\xf0\x9f", false, "abc"},
		{"tail inside 😀 bytes", "\x98\x80xyz", true, "xyz"},
		{"head between 😀 escapes", `abc\ud83d`, false, "abc"},
		{"head inside 😀 escape", `abc\ud83d\ude`, false, "abc"},
		{"tail between 😀 escapes", `\ude00xyz`, true, "xyz"},
		{"tail inside 😀 escape", `d83d\ude00xyz`, true, "xyz"},
		{"head inside u escape", `abc\u00`, false, "abc"},
		{"head trailing backslash", `abc\`, false, "abc"},
		{"head escaped backslash", `abc\\`, false, `abc\`},
		{"tail escaped quote cut", `"xyz`, true, "xyz"},
		{"tail escaped backslash cut", `\xyz`, true, "xyz"},
		{"tail escape letter cut", `nxyz`, true, "nxyz"},
		{"whole escapes", `a\"b\né`, false, "a\"b\né"},
	} {
		if got := unquoteFragment([]byte(tc.frag), tc.cutStart); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A long line whose last quote is escaped has no closing quote; its tail is
// kept raw rather than decoded as if the string ended there.
func TestReadAllKeepsUnclosedLongLineRaw(t *testing.T) {
	line := `{"role":"r","type":"STATUS","message":"` + strings.Repeat("x", maxLine) + `\"}`
	entries, err := ReadAll(writeLog(t, line+"\n"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("got %d entries, err %v", len(entries), err)
	}
	if m := entries[0].Message; !strings.HasSuffix(m, `x\"}`) {
		t.Fatalf("tail decoded: %q", m[len(m)-20:])
	}
}
