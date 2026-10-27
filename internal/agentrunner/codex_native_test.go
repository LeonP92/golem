package agentrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in native CLI test against a synthetic loopback Responses server. It
// needs no provider key and never uses the user's backend/account settings.
func TestCodexNativePolicyWithLocalProvider(t *testing.T) {
	binary := os.Getenv("GOLEM_TEST_CODEX_BINARY")
	if binary == "" {
		t.Skip("set GOLEM_TEST_CODEX_BINARY for free native CLI e2e")
	}
	repo := t.TempDir()
	home := t.TempDir()
	marker := filepath.Join(repo, "leaked")
	os.MkdirAll(filepath.Join(repo, ".golem"), 0755)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: codex\ntool_policy:\n  allow_worktree_only: true\n"), 0644)
	// The denied command references a live env path and would create a marker.
	os.WriteFile(filepath.Join(repo, ".env"), []byte("synthetic-local-fixture"), 0644)
	command := "cp .env " + marker
	var requests atomic.Int32
	var blocked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), "live env/key") || strings.Contains(string(encoded), "PreToolUse") {
			blocked.Store(true)
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event map[string]any) {
			b, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_local", "status": "in_progress", "output": []any{}}})
		var item map[string]any
		if n == 1 {
			args, _ := json.Marshal(map[string]any{"cmd": command})
			item = map[string]any{"type": "function_call", "id": "fc_local", "call_id": "call_local", "name": "exec_command", "arguments": string(args)}
		} else {
			item = map[string]any{"type": "message", "id": "msg_local", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}
		}
		emit(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_local", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
	}))
	defer server.Close()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("HOME", home)
	opts := Options{RepoRoot: repo, Command: binary, ExtraArgs: []string{"--ignore-user-config", "-c", `model_provider="local"`, "-c", `model_providers.local={name="Local",base_url="` + server.URL + `/v1",wire_api="responses",requires_openai_auth=false}`}}
	// This construction is internal test plumbing; production rejects extra -c.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := (Codex{opts: opts}).RunAgent(ctx, "reviewer", Context{RolePrompt: "synthetic local tool transport test"}, "local-synthetic")
	if err != nil {
		t.Fatalf("native CLI: %v", err)
	}
	if !blocked.Load() {
		t.Fatalf("hook not exercised: %s", result.Output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("denied operation ran")
	}
	if requests.Load() < 2 {
		t.Fatal("tool rejection not returned to local provider")
	}
}
