package urlnorm

import (
	"net/url"
	"strings"
)

// Normalize returns a canonical form of a git remote URL:
// scheme and host are lowercased, trailing ".git" and "/" are stripped,
// and query and fragment are removed. The operation is idempotent.
func Normalize(raw string) string {
	u, err := url.Parse(strings.ToLower(raw))
	if err != nil {
		return strings.ToLower(raw)
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	u.Fragment = ""
	u.RawQuery = ""
	return u.String()
}
