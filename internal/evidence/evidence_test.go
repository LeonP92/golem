package evidence

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvidenceBoundaries(t *testing.T) {
	t.Setenv("TEST_API_TOKEN", "sentinel-secret-987654")
	raw := "bare sentinel-secret-987654\nDB_PASSWORD=unseen-secret\n-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"
	cleaned := Clean(raw)
	for _, s := range []string{"sentinel-secret-987654", "unseen-secret", "\nabc\n"} {
		if strings.Contains(cleaned, s) {
			t.Fatalf("secret escaped: %s", cleaned)
		}
	}
	var out bytes.Buffer
	w := NewWriter(&out)
	w.Write([]byte("sentinel-secret-"))
	if out.Len() != 0 {
		t.Fatal("partial write escaped")
	}
	w.Write([]byte("987654\n"))
	w.Flush()
	if strings.Contains(out.String(), "sentinel-secret") {
		t.Fatal(out.String())
	}
	diff := "diff --git a/.env b/.env\n+SECRET=hidden\ndiff --git a/main.go b/main.go\n+safe\n"
	if got := Clean(diff); strings.Contains(got, "hidden") || !strings.Contains(got, "+safe") {
		t.Fatal(got)
	}
}
func TestSensitivePaths(t *testing.T) {
	for _, p := range []string{".env", "x/.env.local", "id_rsa", "a.pem", ".git-credentials", ".netrc", ".npmrc", "id_ecdsa", "id_dsa", ".envrc", "prod.env"} {
		if !SensitivePath(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{".env.example", "x/.env.sample", ".env.template", "main.go"} {
		if SensitivePath(p) {
			t.Fatal(p)
		}
	}
}
func TestMultilineKeyAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	for _, p := range []string{"-----BEGIN PRIVATE KEY-----\n", "SENTINELKEYBODY\n", "-----END PRIVATE KEY-----\n"} {
		w.Write([]byte(p))
	}
	w.Flush()
	if strings.Contains(out.String(), "SENTINELKEYBODY") {
		t.Fatal(out.String())
	}
}
func TestOversizedEvidenceDoesNotEscape(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	w.Write([]byte(strings.Repeat("x", (1<<20)+1) + "sentinel-omitted\n"))
	w.Flush()
	if strings.Contains(out.String(), "sentinel-omitted") || !strings.Contains(out.String(), "omitted") {
		t.Fatal("oversized evidence not omitted")
	}
}
func TestOverlappingSecretsUseLongestFirst(t *testing.T) {
	t.Setenv("SHORT_API_TOKEN", "sentinel-overlap")
	t.Setenv("LONG_API_TOKEN", "sentinel-overlap-and-more")
	if got := Clean("sentinel-overlap-and-more"); got != "[REDACTED]" {
		t.Fatal(got)
	}
}

func TestCleanKeepsProseAndCodeAroundCredentialNames(t *testing.T) {
	for _, keep := range []string{
		`{"route":"human","reason":"missing API token: CI lacks it"}`,
		`+ token := os.Getenv("X")`,
		`password = os.Getenv("DB_PASSWORD")`,
		`credentials = loadCredentialsFromDisk()`,
		`const maxTokens = 4096`,
		`unexpected token: got "}"`,
		`{"input_tokens": 12}`,
	} {
		if got := Clean(keep); got != keep {
			t.Fatalf("prose or code was redacted: %q -> %q", keep, got)
		}
	}
	for _, redact := range []string{`API_TOKEN=abcdef123456`, `password: "hunter2hunter2"`, `Authorization: Bearer abcdef0123456789`} {
		if got := Clean(redact); strings.Contains(got, "abcdef") || strings.Contains(got, "hunter2") {
			t.Fatalf("secret escaped: %q -> %q", redact, got)
		}
	}
}

func TestKeyMarkerMidLineIsNotAKey(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	w.Write([]byte(`if strings.HasPrefix(s, "-----BEGIN RSA PRIVATE KEY-----") {` + "\nlater log line\n"))
	w.Flush()
	if !strings.Contains(out.String(), "later log line") {
		t.Fatal(out.String())
	}
}

func TestMissingKeyEndStopsRedacting(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	w.Write([]byte("-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("body\n", maxKeyLines+5) + "visible after limit\n"))
	w.Flush()
	got := out.String()
	if strings.Count(got, "[PRIVATE KEY REDACTION LIMIT REACHED]") != 1 || !strings.Contains(got, "visible after limit") {
		t.Fatal(got)
	}
}
