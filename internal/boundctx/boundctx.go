// Package boundctx keeps an agent prompt under a model's context window.
//
// A ticket log, a command's output or a diff can be megabytes; one such prompt
// was rejected as "~1241087 tokens (limit 1000000)". A Set bounds each input:
// one that fits is passed through untouched, one that does not is written in
// full to a file and replaced by a head and tail excerpt, the path, and a line
// saying the agent may read the file with its normal tools.
package boundctx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultPerInput bounds one log entry or output in the prompt.
	DefaultPerInput = 24 << 10
	// DefaultTotal bounds all log entries together. The diff is bounded
	// separately by its own limit.
	DefaultTotal = 128 << 10
	// DefaultDiffLimit bounds a diff in the prompt unless the caller says more.
	DefaultDiffLimit = 100 << 10

	// minExcerpt is the least worth showing; below it an input is a pointer.
	minExcerpt = 1 << 10
	topFiles   = 15
)

// Set bounds the inputs of one prompt. The zero value uses the defaults and
// writes no files, so an overflow leaves only the excerpt.
type Set struct {
	// Dir is where full inputs go. A unique prompt-* directory is created in
	// it on the first overflow. Empty means do not write files.
	Dir      string
	PerInput int
	Total    int

	used int
	dir  string
}

func (s *Set) perInput() int {
	if s.PerInput > 0 {
		return s.PerInput
	}
	return DefaultPerInput
}

func (s *Set) total() int {
	if s.Total > 0 {
		return s.Total
	}
	return DefaultTotal
}

// Bound returns text when it fits the per-input limit and the remaining
// total. Otherwise it saves text under name and returns an excerpt with a
// pointer. Name should be short and filename-safe; it is sanitised anyway.
func (s *Set) Bound(name, text string) string {
	room := s.total() - s.used
	if len(text) <= s.perInput() && len(text) <= room {
		s.used += len(text)
		return text
	}
	budget := min(s.perInput(), room)
	if budget < minExcerpt {
		note := s.spill(name, text, 0)
		s.used += len(note)
		return note
	}
	out := Excerpt(text, budget) + s.spill(name, text, len(text))
	s.used += len(out)
	return out
}

// BoundDiff returns diff when it is at most limit bytes. A larger diff becomes
// a per-file summary, an excerpt, and the path of the full diff.
func (s *Set) BoundDiff(name, diff string, limit int) string {
	if limit <= 0 {
		limit = DefaultDiffLimit
	}
	if len(diff) <= limit {
		return diff
	}
	budget := min(s.perInput(), limit)
	return DiffSummary(diff) + "\n" + Excerpt(diff, budget) +
		s.spill(name, diff, len(diff)) +
		"Generated and fixture files (testdata, golden files, lockfiles) can be reviewed by sampling a few of them.\n"
}

// spill writes text to a file and returns the pointer line, or "" when the
// Set has nowhere to write. size is the full length to report; 0 means the
// pointer stands alone.
func (s *Set) spill(name, text string, size int) string {
	path, err := s.write(name, text)
	switch {
	case err != nil && s.Dir != "":
		return fmt.Sprintf("\n[full text of %q not saved: %v]\n", name, err)
	case err != nil && size == 0:
		return fmt.Sprintf("[%s: %d bytes omitted]", name, len(text))
	case err != nil:
		return ""
	case size == 0:
		return fmt.Sprintf("[%s: %d bytes omitted; read the full text at %s with your normal file tools if you need it]", name, len(text), path)
	}
	return fmt.Sprintf("\n[full text, %d bytes, saved at %s; read it with your normal file tools if you need more]\n", size, path)
}

func (s *Set) write(name, text string) (string, error) {
	if s.Dir == "" {
		return "", fmt.Errorf("no directory")
	}
	if s.dir == "" {
		if err := os.MkdirAll(s.Dir, 0o755); err != nil {
			return "", err
		}
		d, err := os.MkdirTemp(s.Dir, "prompt-")
		if err != nil {
			return "", err
		}
		s.dir = d
	}
	path := filepath.Join(s.dir, safeName(name)+".txt")
	for i := 2; ; i++ { // two inputs may share a name
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.WriteString(text)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			return path, werr
		}
		if !os.IsExist(err) {
			return "", err
		}
		path = filepath.Join(s.dir, fmt.Sprintf("%s-%d.txt", safeName(name), i))
	}
}

func safeName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			b[i] = '_'
		}
	}
	if len(b) == 0 {
		return "input"
	}
	return string(b)
}

// Excerpt returns text unchanged when it fits max bytes, and otherwise its
// first and last halves around a marker saying how much was left out. Cuts
// fall on rune boundaries. The result is about max bytes plus the marker.
func Excerpt(text string, max int) string {
	if len(text) <= max {
		return text
	}
	half := max / 2
	head := text[:half]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tail := text[len(text)-half:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	omitted := len(text) - len(head) - len(tail)
	return head + fmt.Sprintf("\n[... %d bytes omitted ...]\n", omitted) + tail
}

// DiffSummary lists the files, line counts and largest files of a unified
// diff, marking generated and fixture files.
func DiffSummary(diff string) string {
	type file struct {
		path      string
		add, del  int
		bytes     int
		generated bool
	}
	var files []*file
	var cur *file
	for _, line := range strings.SplitAfter(diff, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git "); ok {
			path := strings.TrimSpace(rest)
			if i := strings.LastIndex(path, " b/"); i >= 0 {
				path = path[i+3:]
			}
			cur = &file{path: path, generated: generatedPath(path)}
			files = append(files, cur)
		}
		if cur == nil {
			continue
		}
		cur.bytes += len(line)
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			cur.add++
		case strings.HasPrefix(line, "-"):
			cur.del++
		}
	}
	var add, del int
	for _, f := range files {
		add += f.add
		del += f.del
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].bytes > files[j].bytes })
	var b strings.Builder
	fmt.Fprintf(&b, "[diff too large to show whole: %d bytes, %d files, +%d -%d lines. Largest files:]\n", len(diff), len(files), add, del)
	for i, f := range files {
		if i == topFiles {
			fmt.Fprintf(&b, "  ... and %d more\n", len(files)-i)
			break
		}
		tag := ""
		if f.generated {
			tag = " (generated or fixture)"
		}
		fmt.Fprintf(&b, "  %s  +%d -%d  %d bytes%s\n", f.path, f.add, f.del, f.bytes, tag)
	}
	return b.String()
}

func generatedPath(p string) bool {
	for _, s := range []string{"testdata/", "fixtures/", ".golden", ".snap", "package-lock.json", "go.sum", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock"} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}
