package failure

import (
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/evidence"
)

func TestClassifyFailure(t *testing.T) {
	stderrDump := "review gate could not run\n--- stderr ---\n" + strings.Repeat("a 502 line\n", 600)
	for name, c := range map[string]struct{ msg, want string }{
		"codex limit":          {"ERROR: You've hit your usage limit. Upgrade to Pro or try again at 8:00 PM.", UsageLimit},
		"claude limit":         {"You've hit your session limit � resets 5:30pm (UTC)", UsageLimit},
		"bare limit":           {"You've hit your limit", UsageLimit},
		"other agent's limit":  {"Weekly limit reached; try again in 2 days", UsageLimit},
		"limit after a dump":   {stderrDump + "ERROR: you hit your usage limit", UsageLimit},
		"five-hour limit":      {"Claude 5-hour limit reached, try again later", UsageLimit},
		"econnreset":           {"read tcp: ECONNRESET", Transient},
		"anthropic rate error": {`{"type":"rate_limit_error"}`, Transient},
		"rate limiter in code": {"added a rate limiter; FAIL TestRateLimiter", ""},
		"status 500 in a test": {"got status 500, want 200", ""},
		"overloaded":           {"API Error: 503 overloaded", Transient},
		"too many":             {"429 Too Many Requests", Transient},
		"reset":                {"connection reset by peer", Transient},
		"request timeout":      {"request timed out waiting", Transient},
		"status code":          {"error: status 503 from upstream", Transient},
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
		if got := Classify(c.msg); got != c.want {
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
		got, ok := ParseReset(c.msg, now)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
