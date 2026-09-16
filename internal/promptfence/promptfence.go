// Package promptfence neutralizes the structural delimiters a prompt uses to
// separate the operator's instructions from untrusted data.
//
// Every prompt this project builds wraps third-party text — a GitHub issue
// body, a log entry an agent wrote, a diff of files an agent produced — in
// explicit boundary markers with treat-as-data framing. That framing is only
// worth anything if the text inside cannot reproduce the boundary: a body
// containing the close marker appears, to a model reading the prompt as text,
// to end the data region early, so whatever follows it reads as if it sits
// alongside the operator's own instructions.
//
// This package exists because that escaping was previously implemented once,
// in one prompt builder, while the same bytes walked into a second builder one
// process hop away that had no escaping at all. The fence is only as strong as
// the weakest builder the text can reach, so there is exactly one
// implementation here and every builder calls it.
package promptfence

import (
	"fmt"
	"strings"
)

// A Marker is one structural delimiter and the annotation substituted for any
// literal occurrence of it found inside untrusted text.
//
// Replacement is not free-form: see ValidateMarkers for the property it has to
// hold, and why "it looks different from the marker" is not that property.
type Marker struct {
	// Literal is the delimiter exactly as the prompt builder emits it.
	Literal string
	// Replacement is the visible annotation substituted for a literal
	// occurrence of Literal inside untrusted text. It should say plainly
	// that the occurrence is quoted content rather than a real boundary —
	// the model is the audience, not a human reading raw prompt logs.
	Replacement string
}

// Escape returns s with every literal occurrence of every marker replaced by
// that marker's Replacement.
//
// The substitution is a single left-to-right pass over s (one
// strings.Replacer), not a sequence of independent strings.ReplaceAll calls.
// That matters: a sequential pass can match one marker inside the text a
// previous pass just inserted, so the result would depend on the order the
// markers happen to be listed in. One pass never re-examines what it has
// already written.
//
// Callers are responsible for markers that satisfy ValidateMarkers; with
// markers that do, Escape's output provably contains no occurrence of any
// Literal, and Escape is therefore idempotent.
//
// A visible ASCII annotation is used rather than an invisible Unicode
// character (e.g. a zero-width space inserted into the marker). That was
// considered and rejected: it depends on an invisible codepoint surviving,
// byte for byte, a pipeline this code does not control (Go string ->
// exec.Cmd stdin -> the agent CLI -> model input processing), and zero-width
// space is itself a known prompt-injection vector, so any layer in that
// pipeline may strip it as input hygiene — silently reverting the
// substitution to the exact original marker, with no error and no signal.
func Escape(s string, markers ...Marker) string {
	if s == "" || len(markers) == 0 {
		return s
	}
	oldnew := make([]string, 0, 2*len(markers))
	for _, m := range markers {
		oldnew = append(oldnew, m.Literal, m.Replacement)
	}
	return strings.NewReplacer(oldnew...).Replace(s)
}

// ValidateMarkers reports whether a marker set can be escaped without any
// literal marker reappearing in the output — including by reconstitution,
// where leftover marker characters that the match did not consume recombine
// with the replacement text to spell the marker again.
//
// That is not hypothetical. An earlier version of the ticket-description
// markers used a replacement beginning with the bare word
// "TICKET_DESCRIPTION", which is the tail of the open marker
// "<<<TICKET_DESCRIPTION". Because matching is leftmost, a description
// containing six or more leading '<' had only its last three consumed,
// leaving three in front of a replacement that began with the rest of the
// marker — spelling "<<<TICKET_DESCRIPTION" again, byte for byte. Five
// leading '<' was the adjacent safe case, one short of reconstitution. The
// bug was invisible to a test that only asked whether the replacement looked
// different from the marker.
//
// Escape's output is the unmatched source segments interleaved with
// replacements. No complete Literal can lie inside one unmatched segment
// (the replacer tested every position in it), so a surviving Literal must
// span a boundary, which gives exactly four ways for one to appear — and one
// condition each:
//
//  1. wholly inside a replacement          -> no Replacement contains a Literal
//  2. a replacement wholly inside it       -> no Replacement is a substring of a Literal
//  3. replacement tail + following text    -> no non-empty prefix of a Literal is a suffix of a Replacement
//  4. preceding text + replacement head    -> no non-empty suffix of a Literal is a prefix of a Replacement
//
// Conditions 3 and 4 are checked across every (Literal, Replacement) pair,
// not only within a marker, because the text following a replacement is
// attacker-controlled and can be any other marker's characters.
//
// The simplest way to satisfy all four is a replacement that contains none of
// the characters the markers are built from and is longer than every marker:
// then there is nothing on either side for leftover padding to recombine
// with, and reconstitution is structurally impossible rather than merely
// unobserved. This function is what keeps that true as markers change; call
// it from a test over the marker set each prompt builder actually uses.
func ValidateMarkers(markers ...Marker) error {
	if len(markers) == 0 {
		return fmt.Errorf("promptfence: no markers given")
	}
	for i, m := range markers {
		if m.Literal == "" {
			return fmt.Errorf("promptfence: marker %d has an empty Literal", i)
		}
		if m.Replacement == "" {
			return fmt.Errorf("promptfence: marker %q has an empty Replacement", m.Literal)
		}
	}

	for _, r := range markers {
		for _, l := range markers {
			if strings.Contains(r.Replacement, l.Literal) {
				return fmt.Errorf("promptfence: replacement for %q contains the marker %q verbatim",
					r.Literal, l.Literal)
			}
			if strings.Contains(l.Literal, r.Replacement) {
				return fmt.Errorf("promptfence: replacement for %q is a substring of the marker %q, "+
					"so text on both sides of it could spell that marker",
					r.Literal, l.Literal)
			}
			for n := 1; n < len(l.Literal); n++ {
				if strings.HasSuffix(r.Replacement, l.Literal[:n]) {
					return fmt.Errorf("promptfence: replacement for %q ends with %q, a prefix of the "+
						"marker %q — following text could complete it",
						r.Literal, l.Literal[:n], l.Literal)
				}
				if strings.HasPrefix(r.Replacement, l.Literal[len(l.Literal)-n:]) {
					return fmt.Errorf("promptfence: replacement for %q starts with %q, a suffix of the "+
						"marker %q — preceding text could complete it",
						r.Literal, l.Literal[len(l.Literal)-n:], l.Literal)
				}
			}
		}
	}
	return nil
}
