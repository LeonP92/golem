package gating

import (
	"path"
	"strings"
	"unicode"
)

var shellTools = map[string]bool{"bash": true, "sh": true, "zsh": true, "shell": true, "exec_command": true}

// commandSegments returns the token list of every command in the call: for a
// shell tool the args form one command line that is split at ; & | ( ) newlines
// and backticks; any other tool is a single command made of its name and args.
func commandSegments(tool string, args []string) [][]string {
	if shellTools[strings.ToLower(tool)] {
		return splitCommandLine(strings.Join(args, " "), 0)
	}
	return [][]string{append([]string{tool}, args...)}
}

// splitCommandLine tokenizes a shell command line without evaluating it.
// Quotes group words and are removed; separators outside quotes start a new
// command; `sh -c "<script>"` also yields
// the commands of the script. It is a heuristic, not a shell parser.
func splitCommandLine(line string, depth int) [][]string {
	var segments [][]string
	var current []string
	var word strings.Builder
	inWord := false
	var quote rune
	endWord := func() {
		if inWord {
			current = append(current, word.String())
			word.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(current) > 0 {
			segments = append(segments, current)
			current = nil
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case unicode.IsSpace(r) && r != '\n':
			endWord()
		case strings.ContainsRune(";&|()`\n", r):
			endSegment()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endSegment()

	var out [][]string
	for _, seg := range segments {
		out = append(out, seg)
		if depth < 3 && shellTools[strings.ToLower(path.Base(seg[0]))] {
			for i := 1; i+1 < len(seg); i++ {
				if seg[i] == "-c" || seg[i] == "-lc" {
					out = append(out, splitCommandLine(seg[i+1], depth+1)...)
					break
				}
			}
		}
	}
	return out
}
