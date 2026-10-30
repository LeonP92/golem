package worker

import (
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/gate"
)

// Failure kinds the supervisor waits out instead of diagnosing.
const (
	kindUsageLimit = "usage_limit"
	kindTransient  = "transient"
)

// failureTail bounds what classifyFailure reads. The backend's own error comes
// last; a reviewer's diff or a gate's output earlier in the same reason may
// mention limits or status codes as plain text.
const failureTail = 4 << 10

var (
	// Quota exhaustion that clears by itself, in any agent's wording. Plain
	// "quota exhausted" or billing text is not matched: no wait fixes those.
	limitRE = regexp.MustCompile(`(?i)\b(?:usage|session|weekly|daily|monthly|hourly|plan)[ _]limit\b|\bhit your limit\b`)
	// Faults that pass by themselves. A bare "timeout" or number is not enough
	// (TestTimeout, file.go:429): a status code must read as one.
	transientRE = regexp.MustCompile(`(?i)\brate[ _]limit|too many requests|overloaded|bad gateway|service unavailable|gateway time-?out|temporarily unavailable|connection (?:reset|refused)|network error|(?:connection|request|read|dial|tls|i/o) time(?:d )?-?out|\b(?:http|status|error|code)\b[ :=]*(?:429|500|502|503|504|529)\b`)
)

// classifyFailure returns "usage_limit", "transient" or "" (diagnose as usual).
// Environment-marker evidence is structured host output, never a model fault.
func classifyFailure(reason string) string {
	if gate.EnvironmentOutput(reason) {
		return ""
	}
	if strings.Contains(reason, "review gate did not pass") {
		reason, _, _ = strings.Cut(reason, "\n") // a verdict's prose, not an agent error
	}
	if len(reason) > failureTail {
		reason = reason[len(reason)-failureTail:]
	}
	switch {
	case limitRE.MatchString(reason):
		return kindUsageLimit
	case transientRE.MatchString(reason):
		return kindTransient
	}
	return ""
}

// The ways an agent says when it is usable again, tried in this order. They
// are shaped by the wording of the time, never by the agent's name.
var (
	clockPat  = `(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)?`
	zonePat   = `(?:\s*\(([^)]+)\))?`
	delayRE   = regexp.MustCompile(`(?i)\b(?:in|after|retry[ -]after:?)\s*((?:\d+\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b[\s,]*(?:and\s+)?)+)`)
	delayUnit = regexp.MustCompile(`(?i)(\d+)\s*([dhms])`)
	isoRE     = regexp.MustCompile(`\d{4}-\d\d-\d\d[T ]\d\d:\d\d(?::\d\d)?(?:\.\d+)?(?:Z|[+-]\d\d:?\d\d)?`)
	dateRE    = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(?:(\d{4}),?\s+)?(?:at\s+)?` + clockPat + zonePat)
	clockRE   = regexp.MustCompile(`(?i)\b(?:at|until|by|resets?)\s+` + clockPat + zonePat)
	isoLayout = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"}
	delayMult = map[byte]time.Duration{'d': 24 * time.Hour, 'h': time.Hour, 'm': time.Minute, 's': time.Second}
)

// parseLimitReset extracts the instant a usage limit lifts from an agent's
// message: a delay ("in 3 hours 20 minutes"), an ISO timestamp, a dated clock
// time ("Nov 4th, 2026 12:39 PM") or a bare clock time (its next occurrence).
// A parenthesised zone name applies to the time before it; without one the
// shem's own zone does. It reports false for anything it cannot place, so the
// caller backs off instead.
func parseLimitReset(msg string, now time.Time) (time.Time, bool) {
	if at := limitRE.FindAllStringIndex(msg, -1); len(at) > 0 {
		msg = msg[at[len(at)-1][0]:] // the last limit message; earlier text may be quoted output
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	t, ok := resetTime(msg, now)
	return t, ok && t.After(now)
}

func resetTime(msg string, now time.Time) (time.Time, bool) {
	if m := delayRE.FindStringSubmatch(msg); m != nil {
		var d time.Duration
		for _, u := range delayUnit.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(u[1])
			d += time.Duration(n) * delayMult[strings.ToLower(u[2])[0]]
		}
		return now.Add(d), d > 0
	}
	if s := isoRE.FindString(msg); s != "" {
		for _, layout := range isoLayout {
			if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	if m := dateRE.FindStringSubmatch(msg); m != nil {
		loc, ok := zoneOf(m[7], now.Location())
		hour, min, ok2 := clockOf(m[4], m[5], m[6])
		if !ok || !ok2 {
			return time.Time{}, false
		}
		day, _ := strconv.Atoi(m[2])
		year, mon := now.In(loc).Year(), strings.Index("janfebmaraprmayjunjulaugsepoctnovdec", strings.ToLower(m[1]))/3+1
		if m[3] != "" {
			year, _ = strconv.Atoi(m[3])
		}
		t := time.Date(year, time.Month(mon), day, hour, min, 0, 0, loc)
		if m[3] == "" && !t.After(now) {
			t = t.AddDate(1, 0, 0)
		}
		return t, true
	}
	if m := clockRE.FindStringSubmatch(msg); m != nil {
		loc, ok := zoneOf(m[4], now.Location())
		hour, min, ok2 := clockOf(m[1], m[2], m[3])
		if !ok || !ok2 {
			return time.Time{}, false
		}
		local := now.In(loc)
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, min, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	return time.Time{}, false
}

// zoneOf resolves a zone name; empty means def.
func zoneOf(name string, def *time.Location) (*time.Location, bool) {
	if strings.TrimSpace(name) == "" {
		return def, true
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	return loc, err == nil
}

// clockOf reads a 12-hour time with am/pm or a 24-hour HH:MM.
func clockOf(h, m, ampm string) (hour, min int, ok bool) {
	hour, _ = strconv.Atoi(h)
	if m != "" {
		min, _ = strconv.Atoi(m)
	}
	if min > 59 {
		return 0, 0, false
	}
	if ampm == "" {
		return hour, min, m != "" && hour <= 23
	}
	if hour < 1 || hour > 12 {
		return 0, 0, false
	}
	hour %= 12
	if strings.EqualFold(ampm[:1], "p") {
		hour += 12
	}
	return hour, min, true
}

// limitBreaker holds each backend's "usable again at" time. The clock is
// injectable. It is only a cache: the server keeps the shem's limit
// on its row, so a restarted shem re-learns it on its next poll.
type limitBreaker struct {
	mu    sync.Mutex
	now   func() time.Time
	until map[string]time.Time
}

func newLimitBreaker() *limitBreaker {
	return &limitBreaker{now: time.Now, until: map[string]time.Time{}}
}

// trip records that backend is limited until t, keeping the later time, and
// logs once per extension.
func (b *limitBreaker) trip(backend string, t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !t.After(b.now()) || !t.After(b.until[backend]) {
		return
	}
	b.until[backend] = t
	log.Printf("worker: backend %q limited until %s; not claiming new work", backend, t.Format(time.RFC3339))
}

// limited reports whether backend is held right now.
func (b *limitBreaker) limited(backend string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.until[backend].After(b.now())
}
