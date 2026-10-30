package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

func TestClassifyFailure(t *testing.T) {
	stderrDump := "review gate could not run\n--- stderr ---\n" + strings.Repeat("a 502 line\n", 600)
	for name, c := range map[string]struct{ msg, want string }{
		"codex limit":          {"ERROR: You've hit your usage limit. Upgrade to Pro or try again at 8:00 PM.", kindUsageLimit},
		"claude limit":         {"You've hit your session limit � resets 5:30pm (UTC)", kindUsageLimit},
		"bare limit":           {"You've hit your limit", kindUsageLimit},
		"other agent's limit":  {"Weekly limit reached; try again in 2 days", kindUsageLimit},
		"limit after a dump":   {stderrDump + "ERROR: you hit your usage limit", kindUsageLimit},
		"five-hour limit":      {"Claude 5-hour limit reached, try again later", kindUsageLimit},
		"econnreset":           {"read tcp: ECONNRESET", kindTransient},
		"rate limiter in code": {"added a rate limiter; FAIL TestRateLimiter", ""},
		"status 500 in a test": {"got status 500, want 200", ""},
		"overloaded":           {"API Error: 503 overloaded", kindTransient},
		"too many":             {"429 Too Many Requests", kindTransient},
		"reset":                {"connection reset by peer", kindTransient},
		"request timeout":      {"request timed out waiting", kindTransient},
		"status code":          {"error: status 503 from upstream", kindTransient},
		"test name and line":   {"FAIL internal/x/y_test.go:429: TestTimeout 502 lines", ""},
		"test timeout":         {"panic: test timed out after 10m0s", ""},
		"limit far above tail": {"diff says usage limit\n" + strings.Repeat("x", 5000), ""},
		"quota":                {"quota exhausted", ""},
		"billing":              {"billing disabled", ""},
		"login":                {"Not logged in · Please run /login", ""},
		"denied":               {"permission denied", ""},
		"gate":                 {"gate command returned exit 2", ""},
		"reviewer":             {"reviewer FAIL: assertion missing", ""},
		"verdict prose":        {"review gate did not pass\nreviewer failed: 503 from gateway", ""},
		"environment":          {evidence.EnvironmentMarker + ` environment command="setup" exit=1`, ""},
	} {
		if got := classifyFailure(c.msg); got != c.want {
			t.Errorf("%s: classifyFailure = %q, want %q", name, got, c.want)
		}
	}
}

func TestParseLimitReset(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 29, 15, 0, 0, 0, ny) // 19:00 UTC
	at := func(loc *time.Location, d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	for _, c := range []struct {
		name, msg string
		want      time.Time
		ok        bool
	}{
		{"codex, later today (local)", "ERROR: You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro) or try again at 8:00 PM.", at(ny, 29, 20, 0), true},
		{"codex, already past means tomorrow", "ERROR: You've hit your usage limit. try again at 8:00 AM.", at(ny, 30, 8, 0), true},
		{"claude, utc, past means tomorrow", "You've hit your session limit \u00b7 resets 5:30pm (UTC)", at(time.UTC, 30, 17, 30), true},
		{"claude, mangled dot", "You've hit your session limit \ufffd resets 11:30pm (UTC)", at(time.UTC, 29, 23, 30), true},
		{"claude, ellipsis", "You've hit your session limit ... resets 10:40pm (UTC)", at(time.UTC, 29, 22, 40), true},
		{"claude, named zone", "You've hit your session limit \u00b7 resets 9pm (America/New_York)", at(ny, 29, 21, 0), true},
		{"claude, unknown zone", "You've hit your session limit \u00b7 resets 9pm (Mars/Olympus)", time.Time{}, false},
		{"noon", "usage limit, try again at 12:00 PM.", at(ny, 30, 12, 0), true},
		{"codex, dated with ordinal and year", "ERROR: You've hit your usage limit. Upgrade to Pro or try again at Nov 4th, 2026 12:39 PM.", at(ny, 4, 12, 39).AddDate(0, 1, 0), true},
		{"dated, no year, long month", "usage limit reached, resets on October 31 at 9:15 am (UTC)", at(time.UTC, 31, 9, 15), true},
		{"dated, no year, already past means next year", "usage limit, try again at Oct 1, 9:00 AM", time.Date(2027, 10, 1, 9, 0, 0, 0, ny), true},
		{"relative", "Usage limit reached. Try again in 3 hours 20 minutes.", now.Add(3*time.Hour + 20*time.Minute), true},
		{"relative, days and abbreviations", "weekly limit hit; resets in 2d 4h", now.Add(52 * time.Hour), true},
		{"retry-after", "daily limit exceeded, retry after 90 seconds", now.Add(90 * time.Second), true},
		{"iso with zone", "usage limit until 2026-11-04T12:39:00Z", time.Date(2026, 11, 4, 12, 39, 0, 0, time.UTC), true},
		{"iso without zone is shem-local", "usage limit until 2026-10-30 08:00", at(ny, 30, 8, 0), true},
		{"24-hour clock", "session limit, resets at 23:30 (UTC)", at(time.UTC, 29, 23, 30), true},
		{"24-hour clock, past means tomorrow", "session limit, resets at 02:30 (UTC)", at(time.UTC, 30, 2, 30), true},
		{"last limit message wins", "quoted: usage limit try again at 1:00 AM\nERROR: usage limit, try again at 8:00 PM", at(ny, 29, 20, 0), true},
		{"time already past", "usage limit until 2026-10-01T00:00:00Z", time.Time{}, false},
		{"bare number is not a time", "usage limit hit after 3 attempts at 7", time.Time{}, false},
		{"am inside a word is not am", "session limit, resets at 3 amid the outage", time.Time{}, false},
		{"limit without a time", "You've hit your usage limit", time.Time{}, false},
		{"garbage", "something broke \x00 \u2603 try again later", time.Time{}, false},
		{"empty", "", time.Time{}, false},
	} {
		got, ok := parseLimitReset(c.msg, now)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestLimitBreakerPerBackendWithClock(t *testing.T) {
	b := newLimitBreaker()
	clock := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	b.trip("codex", clock.Add(3*time.Hour))
	if !b.limited("codex") || b.limited("claude-code") {
		t.Fatal("breaker must hold only the limited backend")
	}
	b.trip("codex", clock.Add(time.Hour)) // an earlier time never shortens the hold
	clock = clock.Add(2 * time.Hour)
	if !b.limited("codex") {
		t.Fatal("released early")
	}
	clock = clock.Add(time.Hour + time.Second)
	if b.limited("codex") {
		t.Fatal("still held after the reset")
	}
}

// supervisorStack builds the e2e lane: real orchestrator, real SQLite, one
// owned ticket with a saved checkpoint, plus its local worktree fixture.
func supervisorStack(t *testing.T, reason string, manual bool) (*laneStack, db.Ticket, *config.Config) {
	t.Helper()
	remote := "local://supervisor"
	stack := newLaneStack(t, remote)
	repo := t.TempDir()
	dir := filepath.Join(repo, ".golem", "tickets", "supervisor-123")
	wt := filepath.Join(dir, "worktree")
	os.MkdirAll(wt, 0755)
	git(t, wt, "init")
	git(t, wt, "config", "user.email", "test@example.invalid")
	git(t, wt, "config", "user.name", "test")
	os.WriteFile(filepath.Join(wt, "code"), []byte("before\n"), 0644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "base")
	head, err := worktreeHead(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	phase := "implement"
	var shem db.Shem
	stack.gdb.First(&shem)
	tk := db.Ticket{ID: "supervisor-123", RepoRemote: remote, Branch: "ticket/supervisor", Description: "fix handler",
		Phase: phase, ManualApproval: manual, AssignedShem: &shem.ID, CheckpointPhase: &phase, CheckpointSHA: &head}
	if err := stack.gdb.Create(&tk).Error; err != nil {
		t.Fatal(err)
	}
	if err := stack.shem.PostFailure(tk.ID, reason); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Repos: []config.RepoConfig{{Path: repo, NormalizedRemote: remote}}}
	return stack, tk, cfg
}

func TestSupervisorWaitsOutTransientFault(t *testing.T) {
	stack, tk, cfg := supervisorStack(t, "API Error: 503 overloaded", false)
	runs := 0
	worker := New(cfg, stack.shem, &attentionFixer{runs: &runs, t: t}, &Agent{})
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	var fresh db.Ticket
	stack.gdb.First(&fresh, "id = ?", tk.ID)
	if runs != 0 || fresh.Phase != "needs-attention" || !fresh.AttentionEligible ||
		fresh.AttentionWaitUntil == nil || fresh.AttentionWaitChecks != 1 {
		t.Fatalf("not waiting: runs=%d %+v", runs, fresh)
	}
	// Not due yet: no dispatch, no claim.
	if listed, err := stack.shem.GetAttention(); err != nil || len(listed) != 0 {
		t.Fatalf("waiting ticket dispatched: %+v %v", listed, err)
	}
	// Due: the resume retries the saved step without spending a new attempt.
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("attention_wait_until", time.Now().Add(-time.Minute))
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	if runs != 1 {
		t.Fatalf("resume did not run the ticket: %d", runs)
	}
	var resumed db.Ticket
	stack.gdb.First(&resumed, "id = ?", tk.ID)
	if resumed.Phase != "implement" || resumed.AttentionAttempts != 0 || resumed.AttentionWaitUntil != nil {
		t.Fatalf("bad resume: %+v", resumed)
	}
	// The same transient fault again continues the wait instead of tripping
	// the same-failure-twice stop.
	if err := stack.shem.PostFailure(tk.ID, "API Error: 503 overloaded"); err != nil {
		t.Fatal(err)
	}
	stack.gdb.First(&fresh, "id = ?", tk.ID)
	if !fresh.AttentionEligible || strings.Contains(fresh.AttentionReason, "Same failure twice") {
		t.Fatalf("wait repeat stopped: %+v", fresh)
	}
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	stack.gdb.First(&fresh, "id = ?", tk.ID)
	if fresh.AttentionWaitChecks != 2 || fresh.AttentionAttempts != 0 {
		t.Fatalf("wait did not continue: %+v", fresh)
	}
}

func TestSupervisorWaitBudgetEscalatesToHuman(t *testing.T) {
	stack, tk, cfg := supervisorStack(t, "request timed out", false)
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Updates(map[string]any{
		"attention_wait_checks": 5, "attention_wait_fingerprint": stackTicket(t, stack, tk.ID).AttentionFingerprint,
		"attention_wait_deadline": time.Now().Add(10 * time.Minute)})
	runs := 0
	worker := New(cfg, stack.shem, &attentionFixer{runs: &runs, t: t}, &Agent{})
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	fresh := stackTicket(t, stack, tk.ID)
	if runs != 0 || fresh.AttentionEligible || fresh.Phase != "needs-attention" ||
		!strings.Contains(fresh.AttentionReason, "wait budget") {
		t.Fatalf("not escalated: runs=%d %+v", runs, fresh)
	}
}

func stackTicket(t *testing.T, stack *laneStack, id string) db.Ticket {
	t.Helper()
	var fresh db.Ticket
	if err := stack.gdb.First(&fresh, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return fresh
}

func TestSupervisorSkipsManualTickets(t *testing.T) {
	stack, tk, cfg := supervisorStack(t, "reviewer failed: missing assertion", true)
	runs := 0
	worker := New(cfg, stack.shem, &attentionFixer{runs: &runs, t: t}, &Agent{})
	worker.diagnoser = scriptedDiagnoser{route: "retry"}
	// The poll surface never offers a manual ticket; a direct attempt gets
	// the same refusal from the server guard.
	if listed, err := stack.shem.GetAttention(); err != nil || len(listed) != 0 {
		t.Fatalf("manual ticket offered: %+v %v", listed, err)
	}
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	fresh := stackTicket(t, stack, tk.ID)
	if runs != 0 || fresh.Phase != "needs-attention" {
		t.Fatalf("manual ticket recovered: runs=%d %+v", runs, fresh)
	}
}

// countRunner counts runs of any ticket it is handed.
type countRunner struct{ runs *int }

func (e countRunner) RunTicket(context.Context, *config.Config, *client.Client, *client.ClaimResponse) error {
	*e.runs++
	return nil
}

// Replay of the measured failure: the backend is limited for three hours and
// three codex-pinned tickets are waiting. None may be claimed or spent, nothing
// needs a human, and they run once the reset passes.
func TestReplayLimitedBackendHoldsClaimsAndResumes(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Minute).Add(time.Minute)
	msg := "ERROR: You've hit your usage limit. Upgrade to Pro or try again at " + reset.Format("3:04 PM") + "."
	stack, tk, cfg := supervisorStack(t, msg, false)
	stack.gdb.Model(&db.Shem{}).Where("1 = 1").Update("backend", "codex")
	var queued []string
	for i := range 3 {
		u := db.Ticket{ID: fmt.Sprintf("queued-%d", i), RepoRemote: "local://supervisor", Branch: fmt.Sprintf("ticket/q%d", i),
			Description: "next", Phase: "unassigned", ModelBackend: "codex"}
		if err := stack.gdb.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		queued = append(queued, u.ID)
	}
	runs := 0
	worker := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
	clock := time.Now()
	worker.breaker.now = func() time.Time { return clock }

	// The first ticket's phase fails with the limit: it waits, and the backend is held.
	worker.failTicket(tk.ID, fmt.Errorf("%s", msg))
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	first := stackTicket(t, stack, tk.ID)
	if first.AttentionWaitKind != "usage_limit" || first.AttentionAttempts != 0 || first.AttentionWaitUntil == nil || !worker.held() {
		t.Fatalf("not waiting on the limit: held=%v %+v", worker.held(), first)
	}
	for _, id := range queued {
		worker.tryClaimAndRun(id)
	}
	if runs != 0 {
		t.Fatalf("held backend ran %d tickets", runs)
	}
	for _, id := range queued {
		if q := stackTicket(t, stack, id); q.Phase != "unassigned" || q.AssignedShem != nil || q.AttentionAttempts != 0 {
			t.Fatalf("queued ticket touched: %+v", q)
		}
	}
	// A restarted shem re-learns the hold from the server alone.
	fresh := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
	fresh.syncLimit()
	if !fresh.held() {
		t.Fatal("restart lost the limit")
	}

	// Time passes: the wait comes due, the saved step resumes without a new
	// diagnosis or attempt, and the queued tickets then run.
	clock = clock.Add(4 * time.Hour)
	// The server's clock is real: let the shem's recorded limit lapse with it.
	stack.gdb.Model(&db.Shem{}).Where("1 = 1").Update("limited_until", time.Now().Add(-time.Minute))
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Updates(map[string]any{
		"attention_wait_until": time.Now().Add(-time.Minute), "attention_wait_deadline": time.Now().Add(time.Hour)})
	if worker.held() {
		t.Fatal("held after the reset")
	}
	worker.syncLimit()
	if worker.held() {
		t.Fatal("server still reports the limit after the wait elapsed")
	}
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	if resumed := stackTicket(t, stack, tk.ID); runs != 1 || resumed.Phase != "implement" || resumed.AttentionAttempts != 0 {
		t.Fatalf("resume: runs=%d %+v", runs, resumed)
	}
	for _, id := range queued {
		worker.tryClaimAndRun(id)
	}
	if runs != 4 {
		t.Fatalf("queued tickets did not run after the reset: %d", runs)
	}
}

// A ticket claimed in the instant the backend became limited goes back untouched.
func TestClaimedTicketIsReleasedWhenBackendLimited(t *testing.T) {
	stack, _, cfg := supervisorStack(t, "API Error: 503 overloaded", false)
	u := db.Ticket{ID: "late", RepoRemote: "local://supervisor", Branch: "ticket/late", Description: "x", Phase: "unassigned"}
	stack.gdb.Create(&u)
	runs := 0
	worker := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
	worker.breaker.trip(worker.backend(), time.Now().Add(time.Hour))
	if err := stack.shem.ReleaseTicket("late"); err == nil {
		t.Fatal("released a ticket that was never claimed")
	}
	if _, err := stack.shem.ClaimTicket("late"); err != nil {
		t.Fatal(err)
	}
	if err := stack.shem.ReleaseTicket("late"); err != nil {
		t.Fatal(err)
	}
	if got := stackTicket(t, stack, "late"); got.Phase != "unassigned" || got.AssignedShem != nil || runs != 0 {
		t.Fatalf("not released: %+v", got)
	}
}

func TestSupervisorWaitResumeKeepsAttemptsAfterRepeatedLimit(t *testing.T) {
	msg := "You've hit your session limit \u00b7 resets 11:59pm (UTC)"
	stack, tk, cfg := supervisorStack(t, msg, false)
	runs := 0
	worker := New(cfg, stack.shem, &attentionFixer{runs: &runs, t: t}, &Agent{})
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	if w := stackTicket(t, stack, tk.ID); w.AttentionWaitKind != "usage_limit" || w.AttentionAttempts != 0 {
		t.Fatalf("not waiting: %+v", w)
	}
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("attention_wait_until", time.Now().Add(-time.Minute))
	worker.tryAttention(tk.ID, "local://supervisor", 3)
	if err := stack.shem.PostFailure(tk.ID, msg); err != nil {
		t.Fatal(err)
	}
	if again := stackTicket(t, stack, tk.ID); !again.AttentionEligible || again.AttentionAttempts != 0 {
		t.Fatalf("repeat of the limit spent or stopped: %+v", again)
	}
}

// A limit seen by a failing phase with no supervised wait behind it (a
// manual_approval ticket, or a failed wait route) must still survive a restart.
func TestLimitSeenInFailTicketSurvivesRestart(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Minute).Add(time.Minute)
	msg := "ERROR: You've hit your usage limit. Upgrade to Pro or try again at " + reset.Format("3:04 PM") + "."
	stack, tk, cfg := supervisorStack(t, msg, false)
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("manual_approval", true)
	runs := 0
	worker := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
	worker.failTicket(tk.ID, fmt.Errorf("%s", msg))
	if !worker.held() {
		t.Fatal("not held after the failure")
	}
	if got := stackTicket(t, stack, tk.ID); got.AttentionWaitKind != "" {
		t.Fatalf("a wait was set; the test needs none: %+v", got)
	}
	fresh := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
	if fresh.held() {
		t.Fatal("a new worker starts held without syncing")
	}
	fresh.syncLimit()
	if !fresh.held() {
		t.Fatal("restart lost a limit that never reached a wait")
	}
	fresh.tryClaimAndRun("nothing")
	if runs != 0 {
		t.Fatal("held worker ran work")
	}
}

// A ticket waiting out this shem's limit is preserved on the remote and moved
// to the pool: unfinished work is captured in one commit and pushed without
// force, and the ticket resumes from the pushed commit. Anything unsafe stays.
func TestMigrateWaitingCapturesPushesAndMoves(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Minute).Add(time.Minute)
	msg := "ERROR: You've hit your usage limit. Upgrade to Pro or try again at " + reset.Format("3:04 PM") + "."
	type env struct {
		wt, remote string
		w          *Worker
		cfg        *config.Config
		stack      *laneStack
		id         string
	}
	cases := []struct {
		name    string
		mutate  func(t *testing.T, e *env)
		moved   bool
		changed bool // the checkpoint moves to a capture commit
		check   func(t *testing.T, e *env)
	}{
		{"clean, remote lacks it: pushed then moved", func(*testing.T, *env) {}, true, false,
			func(t *testing.T, e *env) {
				if git(t, e.remote, "rev-parse", "refs/heads/ticket/supervisor") == "" {
					t.Error("branch not pushed")
				}
			}},
		{"dirty tracked file is captured", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "code"), []byte("changed\n"), 0644)
		}, true, true, func(t *testing.T, e *env) {
			if got := git(t, e.remote, "show", "refs/heads/ticket/supervisor:code"); !strings.Contains(got, "changed") {
				t.Errorf("remote lacks the edit: %q", got)
			}
		}},
		{"git-ignored file stays out of the capture", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, ".gitignore"), []byte("private.txt\n"), 0644)
			git(t, e.wt, "add", ".gitignore")
			git(t, e.wt, "commit", "-m", "ignore")
			os.WriteFile(filepath.Join(e.wt, "private.txt"), []byte("s3cret\n"), 0644)
			os.WriteFile(filepath.Join(e.wt, "new.go"), []byte("package x\n"), 0644)
		}, true, true, func(t *testing.T, e *env) {
			files := git(t, e.remote, "ls-tree", "-r", "--name-only", "refs/heads/ticket/supervisor")
			if strings.Contains(files, "private.txt") || !strings.Contains(files, "new.go") {
				t.Errorf("captured tree wrong: %s", files)
			}
		}},
		{"secret-looking untracked file refuses the capture", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, ".env"), []byte("KEY=1\n"), 0644)
		}, false, false, func(t *testing.T, e *env) {
			if staged := git(t, e.wt, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
				t.Errorf("index left dirty: %q", staged)
			}
		}},
		{"user's own staging survives a refused capture", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "staged.txt"), []byte("mine\n"), 0644)
			git(t, e.wt, "add", "staged.txt")
			os.WriteFile(filepath.Join(e.wt, ".env"), []byte("KEY=1\n"), 0644)
		}, false, false, func(t *testing.T, e *env) {
			if staged := git(t, e.wt, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "staged.txt" {
				t.Errorf("prior staging changed: %q", staged)
			}
		}},
		{"credential in the diff refuses the capture", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "config.txt"), []byte("api_key = \"abcd1234efgh5678\"\n"), 0644)
		}, false, false, nil},
		{"binary file refuses the capture", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0644)
		}, false, false, nil},
		{"sensitive file the agent already committed is not pushed", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, ".env"), []byte("KEY=1\n"), 0644)
			git(t, e.wt, "add", "-f", ".env")
			git(t, e.wt, "commit", "-m", "oops")
		}, false, false, func(t *testing.T, e *env) {
			if heads := git(t, e.remote, "branch", "--list"); strings.TrimSpace(heads) != "" {
				t.Errorf("something was pushed: %q", heads)
			}
		}},
		{"secret committed before the checkpoint is not pushed", func(t *testing.T, e *env) {
			base := strings.TrimSpace(git(t, e.wt, "rev-parse", "HEAD"))
			os.WriteFile(filepath.Join(filepath.Dir(e.wt), "state.json"), []byte(`{"branch_base_sha":"`+base+`"}`), 0644)
			os.WriteFile(filepath.Join(e.wt, ".env"), []byte("KEY=1\n"), 0644)
			git(t, e.wt, "add", "-f", ".env")
			git(t, e.wt, "commit", "-m", "oops")
			head := strings.TrimSpace(git(t, e.wt, "rev-parse", "HEAD"))
			e.stack.gdb.Model(&db.Ticket{}).Where("id = ?", e.id).Update("checkpoint_sha", head) // the checkpoint moved past the secret
		}, false, false, func(t *testing.T, e *env) {
			if heads := git(t, e.remote, "branch", "--list"); strings.TrimSpace(heads) != "" {
				t.Errorf("something was pushed: %q", heads)
			}
		}},
		{"ticket stopped between capture and push is not pushed", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "code"), []byte("late\n"), 0644)
			e.w.afterCapture = func() {
				e.stack.gdb.Model(&db.Ticket{}).Where("id = ?", e.id).Updates(map[string]any{"model_backend": "codex"})
			}
		}, false, false, func(t *testing.T, e *env) {
			if heads := git(t, e.remote, "branch", "--list"); strings.TrimSpace(heads) != "" {
				t.Errorf("pushed after the ticket was pinned: %q", heads)
			}
		}},
		{"commit left by a crash is pushed on the next attempt", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "code"), []byte("crash\n"), 0644)
			git(t, e.wt, "commit", "-am", "golem: capture unfinished work (usage limit)")
		}, true, true, nil},
		{"remote branch with other commits is not overwritten", func(t *testing.T, e *env) {
			other := filepath.Join(filepath.Dir(e.remote), "other")
			git(t, filepath.Dir(e.remote), "clone", e.remote, other)
			git(t, other, "config", "user.email", "o@example.invalid")
			git(t, other, "config", "user.name", "o")
			git(t, other, "checkout", "-b", "ticket/supervisor")
			os.WriteFile(filepath.Join(other, "theirs"), []byte("x\n"), 0644)
			git(t, other, "add", ".")
			git(t, other, "commit", "-m", "theirs")
			git(t, other, "push", "origin", "ticket/supervisor")
			os.WriteFile(filepath.Join(e.wt, "code"), []byte("mine\n"), 0644)
		}, false, false, func(t *testing.T, e *env) {
			if got := git(t, e.remote, "ls-tree", "--name-only", "refs/heads/ticket/supervisor"); !strings.Contains(got, "theirs") {
				t.Errorf("remote history was overwritten: %s", got)
			}
		}},
		{"unreachable remote leaves the ticket", func(t *testing.T, e *env) {
			os.WriteFile(filepath.Join(e.wt, "code"), []byte("x\n"), 0644)
			os.RemoveAll(e.remote)
		}, false, false, nil},
		{"no_push mode", func(t *testing.T, e *env) { e.cfg.NoPush = true }, false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stack, tk, cfg := supervisorStack(t, msg, false)
			dir := t.TempDir()
			remote := filepath.Join(dir, "remote.git")
			git(t, dir, "init", "--bare", remote)
			git(t, cfg.Repos[0].Path, "init") // the primary repo the worktree belongs to
			stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("repo_remote", remote)
			stack.gdb.Model(&db.Shem{}).Where("1 = 1").Update("backend", "codex")
			cfg.Repos[0].NormalizedRemote = remote
			runs := 0
			w := New(cfg, stack.shem, countRunner{&runs}, &Agent{})
			w.failTicket(tk.ID, fmt.Errorf("%s", msg))
			w.tryAttention(tk.ID, remote, 3)
			before := stackTicket(t, stack, tk.ID)
			if before.AttentionWaitKind != "usage_limit" {
				t.Fatalf("not waiting on the limit: %+v", before)
			}
			e := &env{wt: filepath.Join(cfg.Repos[0].Path, ".golem", "tickets", tk.ID, "worktree"), remote: remote, w: w, cfg: cfg, stack: stack, id: tk.ID}
			tc.mutate(t, e)
			w.migrateWaiting()
			got := stackTicket(t, stack, tk.ID)
			if !tc.moved {
				if got.Phase != "needs-attention" || got.AssignedShem == nil || got.AttentionWaitKind != "usage_limit" || got.MigratedBackend != "" {
					t.Fatalf("moved when it must stay: %+v", got)
				}
				if tc.check != nil {
					tc.check(t, e)
				}
				return
			}
			if got.Phase != "unassigned" || got.AssignedShem != nil || got.MigratedBackend != "codex" || got.AttentionAttempts != 0 || got.CheckpointSHA == nil {
				t.Fatalf("not moved cleanly: %+v", got)
			}
			head, _ := worktreeHead(context.Background(), e.wt)
			if (*got.CheckpointSHA != *before.CheckpointSHA) != tc.changed {
				t.Fatalf("checkpoint %s -> %s, changed want %v", *before.CheckpointSHA, *got.CheckpointSHA, tc.changed)
			}
			if *got.CheckpointSHA != head || strings.TrimSpace(git(t, e.remote, "rev-parse", "refs/heads/ticket/supervisor")) != head {
				t.Fatalf("resume point %s is not the worktree HEAD %s on the remote", *got.CheckpointSHA, head)
			}
			if tc.check != nil {
				tc.check(t, e)
			}
		})
	}
}

// remoteHasCheckpoint asks the real remote: an ancestor of the branch tip
// passes, an unpushed commit and a missing branch do not.
func TestRemoteHasCheckpointAgainstRealRemote(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git(t, dir, "init", "--bare", remote)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0755)
	git(t, work, "init", "-b", "ticket")
	git(t, work, "config", "user.email", "t@example.invalid")
	git(t, work, "config", "user.name", "t")
	os.WriteFile(filepath.Join(work, "f"), []byte("1\n"), 0644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "one")
	pushed := strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))
	git(t, work, "push", remote, "ticket")
	os.WriteFile(filepath.Join(work, "f"), []byte("2\n"), 0644)
	git(t, work, "commit", "-am", "two")
	local := strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0755)
	git(t, repo, "init")
	ctx := context.Background()
	if !remoteHasCheckpoint(ctx, repo, remote, "ticket", pushed) {
		t.Error("pushed checkpoint not found")
	}
	if remoteHasCheckpoint(ctx, repo, remote, "ticket", local) {
		t.Error("an unpushed commit was reported as remote")
	}
	if remoteHasCheckpoint(ctx, repo, remote, "no-such-branch", pushed) {
		t.Error("a missing branch was reported as remote")
	}
}

// Origin waits out its limit, a same-backend target takes the moved ticket and
// hits a limit too: the ticket does not bounce back, no attempt is spent, and
// it resumes on the target when the clock passes the reset.
func TestReplayBothShemsLimitedTicketDoesNotBounce(t *testing.T) {
	for _, start := range []int{0, 2} {
		t.Run(fmt.Sprintf("attempts %d", start), func(t *testing.T) { replayBothLimited(t, start) })
	}
}

func replayBothLimited(t *testing.T, start int) {
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Minute).Add(time.Minute)
	msg := "ERROR: You've hit your usage limit. Upgrade to Pro or try again at " + reset.Format("3:04 PM") + "."
	stack, tk, cfg := supervisorStack(t, msg, false)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git(t, dir, "init", "--bare", remote)
	git(t, cfg.Repos[0].Path, "init")
	cfg.Repos[0].NormalizedRemote = remote
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("repo_remote", remote)
	stack.gdb.Model(&db.Shem{}).Where("1 = 1").Update("backend", "codex")
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("attention_attempts", start)
	hash, _ := bcrypt.GenerateFromPassword([]byte("b-key"), bcrypt.MinCost)
	repos, _ := json.Marshal([]string{remote})
	stack.gdb.Create(&db.Shem{Name: "target", APIKeyHash: string(hash), Repos: string(repos), Status: "online", Backend: "codex", SupportsEpics: true})

	runsA, runsB := 0, 0
	a := New(cfg, stack.shem, countRunner{&runsA}, &Agent{})
	b := New(cfg, client.New(stack.srv.URL, "b-key", "target"), countRunner{&runsB}, &Agent{})

	a.failTicket(tk.ID, fmt.Errorf("%s", msg))
	a.tryAttention(tk.ID, remote, 3)
	a.migrateWaiting()
	moved := stackTicket(t, stack, tk.ID)
	if moved.Phase != "unassigned" || moved.Migrations != 1 || moved.AttentionAttempts != start {
		t.Fatalf("origin did not hand the ticket over: %+v", moved)
	}
	// The target claims it, hits the same kind of limit, and holds.
	if _, err := b.client.ClaimTicket(tk.ID); err != nil {
		t.Fatalf("target claim: %v", err)
	}
	// The executor would resume the saved phase before the limit shows up.
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Update("phase", "implement")
	b.failTicket(tk.ID, fmt.Errorf("%s", msg))
	b.tryAttention(tk.ID, remote, 3)
	onTarget := stackTicket(t, stack, tk.ID)
	if onTarget.AttentionWaitKind != "usage_limit" || onTarget.AssignedShem == nil || !b.held() {
		t.Fatalf("target is not waiting out its limit: held=%v %+v", b.held(), onTarget)
	}
	if onTarget.AttentionAttempts != start {
		t.Fatalf("two limits must not spend attempts: started %d, now %d", start, onTarget.AttentionAttempts)
	}
	// No bounce: a held target cannot move it again, and the origin is held and has nothing to claim.
	b.migrateWaiting()
	a.syncLimit()
	if !a.held() {
		t.Fatal("origin should still be held")
	}
	if again := stackTicket(t, stack, tk.ID); again.AssignedShem == nil || *again.AssignedShem != *onTarget.AssignedShem || again.Migrations != 1 {
		t.Fatalf("ticket bounced: %+v", again)
	}
	// The clock passes both resets: the target resumes the saved step.
	stack.gdb.Model(&db.Shem{}).Where("1 = 1").Update("limited_until", time.Now().Add(-time.Minute))
	stack.gdb.Model(&db.Ticket{}).Where("id = ?", tk.ID).Updates(map[string]any{
		"attention_wait_until": time.Now().Add(-time.Minute), "attention_wait_deadline": time.Now().Add(time.Hour)})
	b.syncLimit()
	b.tryAttention(tk.ID, remote, 3)
	resumed := stackTicket(t, stack, tk.ID)
	if runsB != 1 || runsA != 0 || resumed.Phase != "implement" || resumed.AttentionAttempts != start {
		t.Fatalf("resume: runsA=%d runsB=%d %+v", runsA, runsB, resumed)
	}
}
