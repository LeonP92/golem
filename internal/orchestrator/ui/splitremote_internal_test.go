package ui

import "testing"

// splitRemote feeds Owner and Name straight into GitHub API calls
// (repos/{owner}/{name}), so whatever it returns decides which repository on
// github.com the orchestrator reads issues from and writes comments to.
//
// It used to take the last two path segments of any string at all. A remote
// pointing anywhere — a look-alike host, a subdomain, an attacker's own
// server — still yielded a plausible owner/name pair, and the orchestrator
// then talked to the github.com repository of that name instead. The remote
// an operator configured and the repository Golem acted on were two different
// things, with nothing on screen showing the difference.
func TestSplitRemote_OnlyAcceptsGitHubDotCom(t *testing.T) {
	cases := []struct {
		name              string
		remote            string
		wantOwner, wantNm string
	}{
		{"canonical", "https://github.com/acme/widgets", "acme", "widgets"},
		{"trailing slash", "https://github.com/acme/widgets/", "acme", "widgets"},
		{"http scheme", "http://github.com/acme/widgets", "acme", "widgets"},

		// Every one of these previously returned ("acme", "widgets") and would
		// have been acted on as github.com/acme/widgets.
		{"look-alike host", "https://github.com.evil.example/acme/widgets", "", ""},
		{"prefix look-alike", "https://notgithub.com/acme/widgets", "", ""},
		{"subdomain", "https://evil.github.com.co/acme/widgets", "", ""},
		{"unrelated host", "https://evil.example/acme/widgets", "", ""},
		{"gitlab", "https://gitlab.com/acme/widgets", "", ""},
		{"no host at all", "acme/widgets", "", ""},
		{"deep path", "https://evil.example/a/b/acme/widgets", "", ""},

		// Shape errors on the right host are still rejected: owner/name must
		// be exactly two segments, or the API path is malformed.
		{"owner only", "https://github.com/acme", "", ""},
		{"empty path", "https://github.com/", "", ""},
		{"extra segments", "https://github.com/acme/widgets/tree/main", "", ""},
		{"empty segment", "https://github.com//widgets", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			owner, name := splitRemote(c.remote)
			if owner != c.wantOwner || name != c.wantNm {
				t.Errorf("splitRemote(%q) = (%q, %q), want (%q, %q)",
					c.remote, owner, name, c.wantOwner, c.wantNm)
			}
		})
	}
}

// A repository whose remote is not on github.com cannot be synced, so saving
// it must fail loudly rather than store a blank owner/name that only fails
// later, at the first API call, from a background ticker.
func TestSplitRemote_SSHFormIsNotSilentlyAccepted(t *testing.T) {
	// urlnorm.Normalize does not rewrite scp-style remotes, so this reaches
	// splitRemote verbatim. It names github.com but is not parseable as a
	// URL with that host, and guessing is what this function no longer does.
	owner, name := splitRemote("git@github.com:acme/widgets.git")
	if owner != "" || name != "" {
		t.Errorf("splitRemote(scp-style) = (%q, %q); want empty so the caller "+
			"reports it rather than deriving a repository by guesswork", owner, name)
	}
}
