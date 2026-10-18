package worker

import (
	"context"
	"strings"
	"testing"
)

// At run start the integration ref does not exist yet: the child must
// still start from the intended commit, resolved from the run's base
// branch. Once the ref exists, it wins — the child builds on its
// predecessors.
func TestResolveBaseSHA(t *testing.T) {
	repo, _, _, origin := pushFixture(t)
	ctx := context.Background()
	mainSHA := strings.TrimSpace(git(t, repo, "rev-parse", "main"))

	// epic/x missing -> fallback to main's commit.
	sha, err := resolveBaseSHA(ctx, repo, origin, "epic/x", "main")
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if sha != mainSHA {
		t.Fatalf("fallback sha = %s, want main %s", sha, mainSHA)
	}

	// Create epic/x with a successor commit on it: now it resolves
	// directly, and the fallback is not consulted.
	work := t.TempDir()
	git(t, work, "init", "-b", "epic/x", ".")
	git(t, work, "commit", "--allow-empty", "-m", "pred work")
	epicSHA := strings.TrimSpace(git(t, work, "rev-parse", "epic/x"))
	git(t, repo, "fetch", work, "+refs/heads/epic/x:refs/remotes/origin/epic/x")

	sha, err = resolveBaseSHA(ctx, repo, "", "epic/x", "main")
	if err != nil {
		t.Fatalf("direct: %v", err)
	}
	if sha != epicSHA {
		t.Fatalf("direct sha = %s, want epic/x %s", sha, epicSHA)
	}

	// An ordinary ticket whose base cannot be fetched starts from HEAD.
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if sha, err := resolveBaseSHA(ctx, repo, origin, "missing", ""); err != nil || sha != head {
		t.Fatalf("missing base = %q, %v; want HEAD %q", sha, err, head)
	}
}

// The first finishing child creates the integration ref at the pinned
// base SHA; later children find it and change nothing. An existing ref is
// never advanced.
func TestEnsureRemoteBaseRef(t *testing.T) {
	repo, _, _, origin := pushFixture(t)
	ctx := context.Background()
	pinned := strings.TrimSpace(git(t, repo, "rev-parse", "main"))

	if err := ensureRemoteBaseRef(ctx, repo, origin, "main", "epic/x", pinned); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := strings.TrimSpace(git(t, repo, "ls-remote", origin, "refs/heads/epic/x"))
	if !strings.HasPrefix(got, pinned) {
		t.Fatalf("remote epic/x = %q, want %s", got, pinned)
	}

	// Advance main, call again with the old pinned SHA: the ref is
	// untouched (it exists, so the call is a no-op).
	git(t, repo, "commit", "--allow-empty", "-m", "later")
	git(t, repo, "push", origin, "main")
	if err := ensureRemoteBaseRef(ctx, repo, origin, "main", "epic/x", pinned); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	got = strings.TrimSpace(git(t, repo, "ls-remote", origin, "refs/heads/epic/x"))
	if !strings.HasPrefix(got, pinned) {
		t.Fatalf("existing ref moved to %q", got)
	}

	// Empty inputs are silent no-ops (local-only setups, missing pin).
	for _, args := range [][5]string{
		{repo, "", "main", "epic/y", pinned},   // no remote
		{repo, origin, "main", "", pinned},     // no ref to create
		{repo, origin, "main", "epic/y", ""},   // no pinned SHA
		{repo, origin, "", "epic/y", pinned},   // no fallback ref to fetch
		{repo, origin, "main", "main", pinned}, // same ref: never self-create
	} {
		if err := ensureRemoteBaseRef(ctx, args[0], args[1], args[2], args[3], args[4]); err != nil {
			t.Fatalf("no-op %v: %v", args, err)
		}
	}
	if out := git(t, repo, "ls-remote", origin, "refs/heads/epic/y"); strings.TrimSpace(out) != "" {
		t.Fatalf("epic/y should not exist: %q", out)
	}
}

// no_push and local:// repos have nothing to fetch the base from; the base
// must resolve from local refs instead of failing every fresh ticket.
func TestBaseFetchRemote(t *testing.T) {
	cases := []struct {
		noPush bool
		remote string
		want   string
	}{
		{false, "https://github.com/o/r", "https://github.com/o/r"},
		{true, "https://github.com/o/r", ""},
		{false, "local://test", ""},
	}
	for _, c := range cases {
		if got := baseFetchRemote(c.noPush, c.remote); got != c.want {
			t.Errorf("baseFetchRemote(%t, %q) = %q, want %q", c.noPush, c.remote, got, c.want)
		}
	}

	// And a local-only repo resolves its base with no remote at all.
	repo, _, _, _ := pushFixture(t)
	want := strings.TrimSpace(git(t, repo, "rev-parse", "main"))
	got, err := resolveBaseSHA(context.Background(), repo, baseFetchRemote(false, "local://test"), "main", "")
	if err != nil || got != want {
		t.Fatalf("local resolve = %q, %v; want %q", got, err, want)
	}
}

// A local-only ordinary ticket whose base branch does not exist keeps the
// pre-pinning behavior: it starts from HEAD instead of failing.
func TestLocalOnlyMissingBaseUsesHEAD(t *testing.T) {
	repo, _, _, _ := pushFixture(t)
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	got, err := resolveBaseSHA(context.Background(), repo, "", "no-such-branch", "")
	if err != nil || got != head {
		t.Fatalf("got %q, %v; want HEAD %q", got, err, head)
	}
}
