package boundctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExcerpt(t *testing.T) {
	tests := []struct {
		name, in string
		max      int
		whole    bool
	}{
		{"empty", "", 10, true},
		{"exact budget", strings.Repeat("a", 10), 10, true},
		{"one over", strings.Repeat("a", 11), 10, false},
		{"multibyte cut", strings.Repeat("é", 50), 11, false},
		{"emoji cut", strings.Repeat("😀", 30), 13, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Excerpt(tt.in, tt.max)
			if tt.whole {
				if got != tt.in {
					t.Fatalf("got %q, want input unchanged", got)
				}
				return
			}
			if !utf8.ValidString(got) {
				t.Fatalf("excerpt is not valid UTF-8: %q", got)
			}
			head, rest, _ := strings.Cut(got, "\n[... ")
			_, tail, _ := strings.Cut(rest, " bytes omitted ...]\n")
			if !strings.HasPrefix(tt.in, head) || !strings.HasSuffix(tt.in, tail) {
				t.Fatalf("not head+tail of the input: %q", got)
			}
		})
	}
}

func TestExcerptKeepsHeadAndTailExactly(t *testing.T) {
	in := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	want := "HEAD" + strings.Repeat("x", 46) + "\n[... 908 bytes omitted ...]\n" + strings.Repeat("x", 46) + "TAIL"
	if got := Excerpt(in, 100); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// Bytes that were never UTF-8 (Latin-1, binary) must not shrink the excerpt.
func TestExcerptInvalidUTF8KeepsItsBudget(t *testing.T) {
	in := strings.Repeat("\xff\xfe caf\xe9 ", 100000)
	got := Excerpt(in, 1000)
	if len(got) < 900 || len(got) > 1100 {
		t.Fatalf("excerpt of invalid UTF-8 is %d bytes, want about 1000", len(got))
	}
}

func TestBoundPassesSmallInputThrough(t *testing.T) {
	s := &Set{Dir: t.TempDir()}
	if got := s.Bound("a", "small"); got != "small" {
		t.Fatalf("got %q", got)
	}
	exact := strings.Repeat("a", DefaultPerInput)
	if got := (&Set{Dir: s.Dir}).Bound("b", exact); got != exact {
		t.Fatalf("an input of exactly PerInput bytes must pass whole")
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("no file expected for inputs that fit: %v", entries)
	}
}

func TestBoundTenMegabytesStaysUnderBudgetAndKeepsFullFile(t *testing.T) {
	big := strings.Repeat("log line with some text\n", 10<<20/24)
	s := &Set{Dir: t.TempDir()}
	got := s.Bound("log-00-gate", big)
	if len(got) > DefaultPerInput+512 {
		t.Fatalf("prompt part is %d bytes, budget %d", len(got), DefaultPerInput)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "log-00-gate-*.txt"))
	if len(files) != 1 || !strings.Contains(got, files[0]) {
		t.Fatalf("pointer missing or wrong (%v)", files)
	}
	if data, _ := os.ReadFile(files[0]); string(data) != big {
		t.Fatalf("saved file differs from the input (%d vs %d bytes)", len(data), len(big))
	}
}

func TestBoundWithNoRoomIsAPointer(t *testing.T) {
	s := &Set{Dir: t.TempDir(), Total: 10}
	got := s.Bound("a", strings.Repeat("z", 100))
	files, _ := filepath.Glob(filepath.Join(s.Dir, "a-*.txt"))
	if len(files) != 1 || !strings.Contains(got, "100 bytes omitted") || !strings.Contains(got, files[0]) {
		t.Fatalf("got %q, files %v", got, files)
	}
	if got := (&Set{Total: 10}).Bound("a", strings.Repeat("z", 100)); got != "[a: 100 bytes omitted]" {
		t.Fatalf("no directory: got %q", got)
	}
}

func TestBoundSpillErrorIsSaid(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	s := &Set{Dir: filepath.Join(blocker, "sub")} // a directory cannot be made under a file
	got := s.Bound("a", strings.Repeat("z", 50<<10))
	if !strings.Contains(got, "not saved") || len(got) > DefaultPerInput+512 {
		t.Fatalf("got %d bytes: ...%q", len(got), got[len(got)-200:])
	}
}

func TestBoundSeqHardTotalAndOneNote(t *testing.T) {
	s := &Set{Dir: t.TempDir()}
	items := make([]Item, 5000)
	for i := range items {
		items[i] = Item{Name: "e", Text: strings.Repeat("y", 20<<10)}
	}
	out := s.BoundSeq(items)
	var total int
	for _, o := range out {
		total += len(o)
	}
	if total > DefaultTotal+512 {
		t.Fatalf("total %d exceeds %d", total, DefaultTotal+512)
	}
	if !strings.Contains(out[0], "4") || !strings.Contains(out[0], "older entries") {
		t.Fatalf("first element is not the aggregate note: %q", out[0])
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "*.txt"))
	if len(files) > 10 { // the aggregate plus the few kept excerpts
		t.Fatalf("%d files for 5000 entries", len(files))
	}
}

func TestBoundSeqKeepsNewestAndOrder(t *testing.T) {
	out := (&Set{}).BoundSeq([]Item{{"a", "one"}, {"b", "two"}, {"c", "three"}})
	if strings.Join(out, ",") != "one,two,three" {
		t.Fatalf("got %v", out)
	}
}

func TestSpillReusesFilesAndPrunesOldOnes(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", 30<<10)
	for range 3 {
		(&Set{Dir: dir}).Bound("x", big)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	if len(files) != 1 {
		t.Fatalf("same input three times made %d files", len(files))
	}
	old := filepath.Join(dir, "old.txt")
	os.WriteFile(old, []byte("old"), 0o644)
	week := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(old, week, week)
	(&Set{Dir: dir}).Bound("y", strings.Repeat("b", 30<<10))
	if _, err := os.Stat(old); err == nil {
		t.Fatal("an 8 day old spill file was not pruned")
	}
}

func TestBoundDiffSummarisesAndSaves(t *testing.T) {
	var d strings.Builder
	d.WriteString("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n")
	d.WriteString("diff --git a/testdata/big.json b/testdata/big.json\n--- a/testdata/big.json\n+++ b/testdata/big.json\n")
	for range 5000 {
		d.WriteString("+{\"key\": \"value value value\"}\n")
	}
	s := &Set{Dir: t.TempDir()}
	got := s.BoundDiff("diff", d.String(), 1000)
	for _, want := range []string{"2 files", "+5001 -1", "testdata/big.json  +5000 -0", "(generated or fixture)", "sampling", "UNSEEN", "Excerpt covers: main.go, testdata/big.json", "Request changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(got) > DefaultPerInput+2048 {
		t.Errorf("prompt part is %d bytes", len(got))
	}
	if whole := s.BoundDiff("diff", "small diff", 1000); whole != "small diff" {
		t.Errorf("a diff under the cap must pass whole, got %q", whole)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "diff-*.txt"))
	if len(files) != 1 || !strings.Contains(got, files[0]) {
		t.Fatalf("diff file missing from prompt: %v", files)
	}
	if data, _ := os.ReadFile(files[0]); string(data) != d.String() {
		t.Error("saved diff differs from the input")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"": "input", "log-00-gate": "log-00-gate", "../../etc/passwd": ".._.._etc_passwd", "a b:c": "a_b_c", "é": "__"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnreadableNeverPointsTheAgentAtFiles(t *testing.T) {
	dir := t.TempDir()
	s := &Set{Dir: dir, Unreadable: true}
	big := strings.Repeat("a", 50<<10)
	out := s.Bound("x", big) + (&Set{Dir: dir, Unreadable: true, Total: 10}).Bound("y", big) +
		(&Set{Dir: dir, Unreadable: true}).BoundDiff("diff", "diff --git a/f b/f\n"+big, 1000)
	if strings.Contains(out, dir) || strings.Contains(out, "you may read") || strings.Contains(out, "Read ") {
		t.Fatalf("prompt points at files the agent cannot open:\n%s", out[:400])
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.txt")); len(files) != 3 {
		t.Fatalf("files are still saved for the operator: %v", files)
	}
}
