package boundctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			if !strings.Contains(got, "bytes omitted") {
				t.Fatalf("no omission marker: %q", got)
			}
			head, tail, _ := strings.Cut(got, "\n[...")
			if !strings.HasPrefix(tt.in, head) || !strings.HasSuffix(tt.in, tail[strings.Index(tail, "...]\n")+5:]) {
				t.Fatalf("excerpt is not head+tail of the input: %q", got)
			}
		})
	}
}

func TestExcerptKeepsHeadAndTail(t *testing.T) {
	in := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	got := Excerpt(in, 100)
	if !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") || len(got) > 140 {
		t.Fatalf("bad excerpt (%d bytes): %q", len(got), got)
	}
}

func TestBoundPassesSmallInputThrough(t *testing.T) {
	s := &Set{Dir: t.TempDir()}
	if got := s.Bound("a", "small"); got != "small" {
		t.Fatalf("got %q", got)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("a small input must not create files: %v", entries)
	}
}

func TestBoundTenMegabytesStaysUnderBudgetAndKeepsFullFile(t *testing.T) {
	big := strings.Repeat("log line with some text\n", 10<<20/24)
	s := &Set{Dir: t.TempDir()}
	got := s.Bound("log-00-gate", big)
	if len(got) > DefaultPerInput+1024 {
		t.Fatalf("prompt part is %d bytes, budget %d", len(got), DefaultPerInput)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "prompt-*", "log-00-gate.txt"))
	if len(files) != 1 || !strings.Contains(got, files[0]) || !strings.Contains(got, "normal file tools") {
		t.Fatalf("pointer missing or wrong (%v): %q", files, got[len(got)-300:])
	}
	if data, _ := os.ReadFile(files[0]); string(data) != big {
		t.Fatalf("saved file differs from the input (%d vs %d bytes)", len(data), len(big))
	}
}

func TestBoundTotalBudgetIsHard(t *testing.T) {
	s := &Set{Dir: t.TempDir()}
	var total int
	for range 200 {
		total += len(s.Bound("e", strings.Repeat("y", 20<<10)))
	}
	if limit := DefaultTotal + 200*300; total > limit {
		t.Fatalf("total %d exceeds %d", total, limit)
	}
}

func TestBoundWithoutDirKeepsAnExcerpt(t *testing.T) {
	s := &Set{}
	got := s.Bound("a", strings.Repeat("z", 1<<20))
	if len(got) > DefaultPerInput+100 || !strings.Contains(got, "omitted") {
		t.Fatalf("got %d bytes", len(got))
	}
	s = &Set{Total: 10}
	if got := s.Bound("a", strings.Repeat("z", 100)); got == "" || !strings.Contains(got, "omitted") {
		t.Fatalf("an input with no room and no directory vanished: %q", got)
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
	for _, want := range []string{"2 files", "+5001 -1", "testdata/big.json", "generated or fixture", "sampling", "saved at"} {
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
	files, _ := filepath.Glob(filepath.Join(s.Dir, "prompt-*", "diff.txt"))
	if data, _ := os.ReadFile(files[0]); string(data) != d.String() {
		t.Error("saved diff differs from the input")
	}
}

func TestSameNameTwiceKeepsBothFiles(t *testing.T) {
	s := &Set{Dir: t.TempDir(), Total: 1 << 30}
	s.Bound("x", strings.Repeat("a", 30<<10))
	s.Bound("x", strings.Repeat("b", 30<<10))
	if files, _ := filepath.Glob(filepath.Join(s.Dir, "prompt-*", "x*.txt")); len(files) != 2 {
		t.Fatalf("got %v", files)
	}
}
