// Package evidence keeps secrets out of persisted and model-facing evidence.
// It is defense in depth, not a filesystem or network sandbox.
package evidence

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A credential assignment whose value looks like a secret: one unbroken token of at least 8 characters. Prose and code after the name
// ("missing API token: CI lacks it", `token := os.Getenv(...)`, `const maxTokens = 4096`) must survive: diagnoser JSON and diffs are model input.
var assignment = regexp.MustCompile(`(?im)((?:[a-z0-9_]*(?:secret|token|password|passwd|api_key|private_key|credential)s?(?:_[a-z0-9_]+)?|authorization)\s*["']?\s*[:=]\s*["']?)(?:(?:bearer|basic)\s+)?([^\s"'(),;{}\[\]]{8,})`)
var privateKey = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)

// SensitivePath excludes live configuration and key material, not templates.
func SensitivePath(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if name == ".env.example" || name == ".env.sample" || name == ".env.template" {
		return false
	}
	switch name {
	case ".env", ".envrc", ".netrc", ".npmrc", ".git-credentials", "credentials", "credentials.json", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa":
		return true
	}
	return strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key")
}

func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, part := range []string{"secret", "token", "password", "passwd", "api_key", "private_key", "credential"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

// Clean redacts known process secrets, credential assignments and private keys,
// and omits entire sensitive-file diff sections before they reach another model.
func Clean(text string) string { return clean(text, processSecrets()) }

// processSecrets lists secret environment values, longest first so an overlapping shorter one cannot leave a tail.
func processSecrets() []string {
	var secrets []string
	for _, pair := range os.Environ() {
		name, value, _ := strings.Cut(pair, "=")
		if secretName(name) && len(value) >= 8 {
			secrets = append(secrets, value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

func clean(text string, secrets []string) string {
	omit := false
	return cleanWithDiff(text, secrets, &omit)
}

// cleanWithDiff shares diff-section state between buffered and streaming evidence.
func cleanWithDiff(text string, secrets []string, omit *bool) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			*omit = false
			for _, p := range strings.Fields(line)[2:] {
				if SensitivePath(strings.Trim(p, "\"\r\n")) {
					*omit = true
				}
			}
			if *omit {
				b.WriteString("[sensitive file diff omitted]\n")
			}
		}
		if !*omit {
			b.WriteString(line)
		}
	}
	text = b.String()
	for _, value := range secrets {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	text = privateKey.ReplaceAllString(text, "[PRIVATE KEY REDACTED]")
	return redactAssignments(text)
}

// redactAssignments replaces only the value, preserving quotes and structured output.
// A value followed by a call (`password = os.Getenv(...)`) is code, not a secret.
func redactAssignments(text string) string {
	var b strings.Builder
	last := 0
	for _, m := range assignment.FindAllStringSubmatchIndex(text, -1) {
		if m[1] < len(text) && text[m[1]] == '(' {
			continue
		}
		b.WriteString(text[last:m[4]])
		b.WriteString("[REDACTED]")
		last = m[5]
	}
	b.WriteString(text[last:])
	return b.String()
}

const maxKeyLines = 200

// Writer buffers lines so a credential split across writes is never emitted in
// pieces. An oversized line is discarded, not flushed unsanitized.
type Writer struct {
	dst      io.Writer
	secrets  []string
	pending  []byte
	dropping bool
	private  bool
	keyLines int
	omitDiff bool
}

func NewWriter(dst io.Writer) *Writer { return &Writer{dst: dst, secrets: processSecrets()} }
func (w *Writer) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i + 1
		}
		if !w.dropping {
			if len(w.pending)+end > 1<<20 {
				w.pending = nil
				w.dropping = true
			} else {
				w.pending = append(w.pending, p[:end]...)
			}
		}
		p = p[end:]
		if i >= 0 {
			if err := w.Flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}
func (w *Writer) Flush() error {
	text := ""
	if w.dropping {
		text = "[oversized evidence line omitted]\n"
	} else {
		line := string(w.pending)
		// Only a line that starts with the marker opens a key; source code that quotes it does not.
		if strings.HasPrefix(strings.TrimSpace(line), "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY-----") {
			w.private = true
			w.keyLines = 0
		}
		if w.private && w.keyLines >= maxKeyLines {
			// The END line may never come; stop hiding the log.
			w.private = false
			text = "[PRIVATE KEY REDACTION LIMIT REACHED]\n"
		}
		if w.private {
			w.keyLines++
			if strings.Contains(line, "-----END ") && strings.Contains(line, "PRIVATE KEY-----") {
				w.private = false
			}
			text = "[PRIVATE KEY LINE REDACTED]\n"
		} else {
			text += cleanWithDiff(line, w.secrets, &w.omitDiff)
		}
	}
	w.pending = nil
	w.dropping = false
	if text == "" {
		return nil
	}
	_, err := io.WriteString(w.dst, text)
	return err
}

// EnvironmentMarker starts the first line of every environment failure, so a
// caller can stop automatic repair for a fault no code change can fix.
const EnvironmentMarker = "GOLEM_ENVIRONMENT_FAILURE:"
