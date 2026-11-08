// Package boundctx keeps an agent prompt under a model's context window.
//
// A ticket log, a command's output or a diff can be megabytes; one such prompt
// was rejected as "~1241087 tokens (limit 1000000)". A Set bounds each input:
// one that fits is passed through untouched, one that does not is written in
// full to a file and replaced by a head and tail excerpt, the path, and a line
// saying the agent may read the file.
package boundctx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	// MaxRolePrompt is the ceiling on the instruction part of a prompt.
	MaxRolePrompt = 64 << 10

	// minExcerpt is the least worth showing; below it an input is a pointer.
	minExcerpt = 1 << 10
	topFiles   = 15
	// pruneAfter is how long spilled files stay.
	pruneAfter = 7 * 24 * time.Hour
)

// Set bounds the inputs of one prompt. The zero value uses the defaults and
// writes no files, so an overflow leaves only the excerpt.
type Set struct {
	// Dir is where full inputs go. Files are named by content, so building the
	// same prompt again reuses them. Empty means do not write files.
	Dir      string
	PerInput int
	Total    int
	// Unreadable means the agent has no tools: files are saved for the operator
	// and the prompt does not point the agent at them.
	Unreadable bool

	used   int
	pruned bool
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

// Item is one named input of a sequence.
type Item struct{ Name, Text string }

// Bound returns text when it fits the per-input limit and the remaining
// total. Otherwise it saves text under name and returns an excerpt with a
// pointer, or only the pointer when little room is left.
func (s *Set) Bound(name, text string) string {
	room := s.total() - s.used
	if len(text) <= s.perInput() && len(text) <= room {
		s.used += len(text)
		return text
	}
	budget := min(s.perInput(), room)
	var out string
	if budget < minExcerpt {
		out = s.pointer(name, text)
	} else {
		out = Excerpt(text, budget) + s.saved(name, text)
	}
	s.used += len(out)
	return out
}

// BoundSeq bounds items, oldest first, spending the budget on the newest. When
// the budget runs out the older items are replaced by a single note at the
// front, with their full text in one file; the result then has one more
// element than the kept items. The total stays within the Set's Total plus
// that one note.
func (s *Set) BoundSeq(items []Item) []string {
	out := make([]string, len(items))
	cut := 0 // items[:cut] are omitted
	for i := len(items) - 1; i >= 0; i-- {
		room := s.total() - s.used
		it := items[i]
		if len(it.Text) <= min(s.perInput(), room) {
			s.used += len(it.Text)
			out[i] = it.Text
			continue
		}
		budget := min(s.perInput(), room)
		if budget < minExcerpt {
			cut = i + 1
			break
		}
		out[i] = Excerpt(it.Text, budget) + s.saved(it.Name, it.Text)
		s.used += len(out[i])
	}
	if cut == 0 {
		return out
	}
	var all strings.Builder
	for _, it := range items[:cut] {
		fmt.Fprintf(&all, "=== %s ===\n%s\n", it.Name, it.Text)
	}
	note := s.pointer(fmt.Sprintf("%d older entries", cut), all.String())
	s.used += len(note)
	return append([]string{note}, out[cut:]...)
}

// BoundDiff returns diff when it is at most limit bytes. A larger diff becomes
// a per-file summary, an excerpt, a statement of what was not seen, and the
// path of the full diff.
func (s *Set) BoundDiff(name, diff string, limit int) string {
	if limit <= 0 {
		limit = DefaultDiffLimit
	}
	if len(diff) <= limit {
		return diff
	}
	excerpt := Excerpt(diff, min(s.perInput(), limit))
	path, _ := s.write(name, diff)
	read := "The full diff is not available to you."
	if path != "" && !s.Unreadable {
		read = "Read " + path + " for the rest."
	}
	return DiffSummary(diff) + "\nExcerpt covers: " + strings.Join(diffFiles(excerpt), ", ") + "\n" +
		"UNSEEN: only this excerpt and the file summary were provided, not the whole diff. " +
		"Request changes for anything you cannot judge from them. " + read + "\n" +
		"Generated and fixture files (testdata, golden files, lockfiles) can be reviewed by sampling a few of them.\n\n" +
		excerpt + "\n"
}

// pointer replaces text entirely.
func (s *Set) pointer(name, text string) string {
	path, err := s.write(name, text)
	if err == nil && s.Unreadable {
		return fmt.Sprintf("[%s: %d bytes omitted; the full text is saved for the operator, not readable in this session]", name, len(text))
	}
	if err != nil {
		return fmt.Sprintf("[%s: %d bytes omitted]", name, len(text))
	}
	return fmt.Sprintf("[%s: %d bytes omitted; the full text is in %s, which you may read with your file or shell tools]", name, len(text), path)
}

// saved is the line after an excerpt; empty when there is nowhere to write.
func (s *Set) saved(name, text string) string {
	path, err := s.write(name, text)
	switch {
	case err == nil && s.Unreadable:
		return fmt.Sprintf("\n[full text, %d bytes, saved for the operator; not readable in this session]\n", len(text))
	case err == nil:
		return fmt.Sprintf("\n[full text, %d bytes, in %s; you may read it with your file or shell tools]\n", len(text), path)
	case s.Dir != "":
		return fmt.Sprintf("\n[full text of %q not saved: %v]\n", name, err)
	}
	return ""
}

// write saves text under a name derived from its content and returns the path.
// Writing the same text again changes nothing, so repeated prompts for one
// ticket do not grow the directory.
func (s *Set) write(name, text string) (string, error) {
	if s.Dir == "" {
		return "", fmt.Errorf("no directory")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	if !s.pruned {
		s.pruned = true
		prune(s.Dir)
	}
	sum := sha256.Sum256([]byte(text))
	path := filepath.Join(s.Dir, safeName(name)+"-"+hex.EncodeToString(sum[:4])+".txt")
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(text)) {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	return path, os.WriteFile(path, []byte(text), 0o644)
}

// prune removes spilled files nobody has asked for in a week.
func prune(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() && time.Since(info.ModTime()) > pruneAfter {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
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
// move to the nearest rune start, so a multi-byte character is dropped whole
// rather than split; bytes that were never valid UTF-8 are left alone.
func Excerpt(text string, max int) string {
	if len(text) <= max {
		return text
	}
	h := max / 2
	for i := 0; i < utf8.UTFMax-1 && h > 0 && !utf8.RuneStart(text[h]); i++ {
		h--
	}
	t := len(text) - max/2
	for i := 0; i < utf8.UTFMax-1 && t < len(text) && !utf8.RuneStart(text[t]); i++ {
		t++
	}
	return text[:h] + fmt.Sprintf("\n[... %d bytes omitted ...]\n", t-h) + text[t:]
}

type fileStat struct {
	path      string
	add, del  int
	bytes     int
	generated bool
}

func diffStats(diff string) []*fileStat {
	var files []*fileStat
	var cur *fileStat
	for _, line := range strings.SplitAfter(diff, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git "); ok {
			path := strings.TrimSpace(rest)
			if i := strings.LastIndex(path, " b/"); i >= 0 {
				path = path[i+3:]
			}
			cur = &fileStat{path: path, generated: generatedPath(path)}
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
	return files
}

// diffFiles names the files whose header appears in a piece of diff.
func diffFiles(excerpt string) []string {
	var names []string
	for _, f := range diffStats(excerpt) {
		names = append(names, f.path)
	}
	if len(names) == 0 {
		return []string{"(parts of one file)"}
	}
	return names
}

// DiffSummary lists the files, line counts and largest files of a unified
// diff, marking generated and fixture files.
func DiffSummary(diff string) string {
	files := diffStats(diff)
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
