package config_test

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/config"
)

// captureLog redirects the standard logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orchestrator.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadReportsBadDurationsEagerly: a duration typo must be visible at
// startup whatever else the deployment is doing.
//
// The warnings used to be emitted by the accessors, which are only called
// from the branch that actually starts GitHub sync. An install with
// "poll_interval: 15mn" and no token, or no enabled repositories, therefore
// started completely silent about both typos — and an install that did start
// sync logged each one twice, once per call site. csp.mode has always
// validated eagerly in Load; the durations now do the same.
func TestLoadReportsBadDurationsEagerly(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantLogs []string
		wantNone []string
	}{
		{
			name: "typo and non-positive value are both reported",
			body: "port: 8080\ngithub:\n  poll_interval: 15mn\n  drain_interval: -5s\n",
			wantLogs: []string{
				"github.poll_interval",
				`"15mn"`,
				"github.drain_interval",
				`"-5s"`,
			},
		},
		{
			name:     "omitted durations are silent",
			body:     "port: 8080\n",
			wantNone: []string{"poll_interval", "drain_interval", "manual_sync_cooldown"},
		},
		{
			name:     "valid durations are silent",
			body:     "port: 8080\ngithub:\n  poll_interval: 5m\n  drain_interval: 10s\n  manual_sync_cooldown: 30s\n",
			wantNone: []string{"poll_interval", "drain_interval", "manual_sync_cooldown"},
		},
		{
			name:     "a bad cooldown is reported too",
			body:     "port: 8080\ngithub:\n  manual_sync_cooldown: 0s\n",
			wantLogs: []string{"github.manual_sync_cooldown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)
			var cfg *config.Config
			out := captureLog(t, func() {
				var err error
				cfg, err = config.Load(path)
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
			})
			for _, want := range tc.wantLogs {
				if !strings.Contains(out, want) {
					t.Errorf("Load logged %q, want it to mention %q", out, want)
				}
			}
			// Exactly one line per bad key: the old accessors logged once
			// per call site. (The bad VALUE can legitimately appear twice
			// on one line, because time.ParseDuration quotes it too.)
			for _, key := range []string{"github.poll_interval", "github.drain_interval", "github.manual_sync_cooldown"} {
				lines := 0
				for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
					if strings.Contains(line, key) {
						lines++
					}
				}
				want := 0
				for _, w := range tc.wantLogs {
					if w == key {
						want = 1
					}
				}
				if lines != want {
					t.Errorf("Load logged %d line(s) about %s, want %d:\n%s", lines, key, want, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("Load logged %q about %q, want silence", out, none)
				}
			}

			// The accessors must be silent afterwards: they are called from
			// several places and each call must not re-log.
			after := captureLog(t, func() {
				cfg.GitHub.PollIntervalDuration()
				cfg.GitHub.DrainIntervalDuration()
				cfg.GitHub.ManualSyncCooldownDuration()
				cfg.GitHub.PollIntervalDuration()
			})
			if after != "" {
				t.Errorf("accessors logged %q after Load already reported it; every call site would duplicate the warning", after)
			}
		})
	}
}

// TestBadDurationsStillFallBack: reporting must not change the value the
// server actually runs with.
func TestBadDurationsStillFallBack(t *testing.T) {
	path := writeConfig(t, "port: 8080\ngithub:\n  poll_interval: 15mn\n  drain_interval: -5s\n  manual_sync_cooldown: nope\n")
	var cfg *config.Config
	captureLog(t, func() {
		var err error
		cfg, err = config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if got := cfg.GitHub.PollIntervalDuration().String(); got != "15m0s" {
		t.Errorf("poll interval = %s, want the 15m0s default", got)
	}
	if got := cfg.GitHub.DrainIntervalDuration().String(); got != "20s" {
		t.Errorf("drain interval = %s, want the 20s default", got)
	}
	if got := cfg.GitHub.ManualSyncCooldownDuration().String(); got != "1m0s" {
		t.Errorf("manual sync cooldown = %s, want the 1m0s default", got)
	}
}
