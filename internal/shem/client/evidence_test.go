package client_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/shem/client"
)

func TestEvidenceUploadsDoNotCarrySentinel(t *testing.T) {
	t.Setenv("SYNTHETIC_API_TOKEN", "sentinel-never-upload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "sentinel-never-upload") {
			t.Errorf("secret in %s body", r.URL.Path)
		}
		if strings.Contains(r.URL.Path, "/log") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":1,"sequence_num":1}`)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	c := client.New(server.URL, "synthetic-auth", "test")
	if _, err := c.PostLog("ticket", client.LogPayload{Message: "sentinel-never-upload"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PostFailure("ticket", "sentinel-never-upload"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spec.md")
	os.WriteFile(path, []byte("sentinel-never-upload"), 0644)
	if _, err := c.PostDocumentFile("ticket", "SPEC", "developer", path); err != nil {
		t.Fatal(err)
	}
	if err := c.PostTicketVerdict("ticket", client.TicketVerdictPayload{Evidence: "sentinel-never-upload"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PostEpicVerdict("epic", client.EpicVerdictPayload{Evidence: "sentinel-never-upload"}); err != nil {
		t.Fatal(err)
	}
}
