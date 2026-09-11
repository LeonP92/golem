// Package slug provides slugification for turning free-text ticket titles
// into filesystem/git-safe branch name components.
package slug

import "strings"

// Slug lowercases s, collapses runs of non [a-z0-9] characters into a single
// '-', trims leading/trailing '-', and truncates to 40 characters (trimming
// any trailing '-' left by truncation). Returns "untitled" if the result is
// empty.
func Slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	if out == "" {
		return "untitled"
	}
	return out
}

// Branch computes the working branch name for a ticket from its title and ID:
// ticket/<slug(title)>-<id[:8]>.
func Branch(title, ticketID string) string {
	suffix := ticketID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "ticket/" + Slug(title) + "-" + suffix
}
