package agentrunner

import (
	"strings"
	"testing"
)

func TestNewUnknownAdapterNamesTheKnownOnes(t *testing.T) {
	_, err := New("nonesuch", Options{})
	if err == nil {
		t.Fatal("an unknown adapter name was accepted")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("the error does not name the known adapters: %v", err)
	}
}

func TestNewClaudeCodeHonoursOptions(t *testing.T) {
	a, err := New("claude-code", Options{
		Command:   "my-claude",
		ExtraArgs: []string{"--add-dir", "/opt/toolchains"},
		Env:       map[string]string{"ANTHROPIC_BASE_URL": "https://proxy.example"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cc, ok := a.(ClaudeCode)
	if !ok {
		t.Fatalf("New returned %T, want ClaudeCode", a)
	}
	argv, err := cc.argv("")
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	want := "my-claude --print --add-dir /opt/toolchains"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
	if cc.opts.Env["ANTHROPIC_BASE_URL"] != "https://proxy.example" {
		t.Errorf("Env was not carried: %v", cc.opts.Env)
	}
}

func TestRegisterRejectsDuplicatesAndNilFactories(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("registering claude-code twice did not panic")
			}
		}()
		Register("claude-code", func(Options) Adapter { return nil })
	})
	t.Run("nil factory", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("a nil factory did not panic")
			}
		}()
		Register("registry-test-nil", nil)
	})
}
