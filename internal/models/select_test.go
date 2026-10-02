package models

import "testing"

func TestValidateSelections(t *testing.T) {
	claude := testCatalog()
	single := Catalog{Models: []Model{{ID: "only"}}}
	onlyOpus := Catalog{SupportsSelection: true, Models: []Model{{ID: "opus"}}}
	onlySonnet := Catalog{SupportsSelection: true, Models: []Model{{ID: "sonnet"}}}
	catalogs := map[string][]Catalog{
		"claude-code": {claude}, "single": {single}, "split": {onlyOpus, onlySonnet},
	}
	tests := []struct {
		name        string
		backend     string
		raw         map[string]string
		wantErr     bool
		wantBound   string
		wantSelKeys int
	}{
		{name: "empty binds no backend", backend: "claude-code", raw: map[string]string{}},
		{name: "empty values are dropped", backend: "claude-code", raw: map[string]string{"plan": ""}},
		{name: "model id with no backend", raw: map[string]string{"plan": "opus"}, wantErr: true},
		{name: "unreported backend", backend: "nope", raw: map[string]string{"plan": "opus"}, wantErr: true},
		{name: "unknown id", backend: "claude-code", raw: map[string]string{"plan": "gpt"}, wantErr: true},
		{name: "unknown tier", backend: "claude-code", raw: map[string]string{"plan": "tier:huge"}, wantErr: true},
		{name: "no selection support", backend: "single", raw: map[string]string{"plan": "only"}, wantErr: true},
		{
			name: "declared tier with a backend binds none", backend: "claude-code",
			raw: map[string]string{"plan": "tier:large"}, wantSelKeys: 1,
		},
		{
			name: "declared tier with no backend binds none",
			raw:  map[string]string{"plan": "tier:large"}, wantSelKeys: 1,
		},
		{name: "tier no catalog declares", raw: map[string]string{"plan": "tier:huge"}, wantErr: true},
		{
			name: "mixed set with one concrete id binds the backend", backend: "claude-code",
			raw:       map[string]string{"plan": "tier:large", "review": "opus"},
			wantBound: "claude-code", wantSelKeys: 2,
		},
		{
			name: "one shem declares the whole set", backend: "split",
			raw: map[string]string{"plan": "opus"}, wantBound: "split", wantSelKeys: 1,
		},
		{
			name: "ids split across shems no one can claim", backend: "split",
			raw: map[string]string{"plan": "opus", "implement": "sonnet"}, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, bound, err := ValidateSelections(catalogs, tt.backend, tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got sel=%v bound=%q", sel, bound)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if bound != tt.wantBound {
				t.Errorf("bound = %q, want %q", bound, tt.wantBound)
			}
			if len(sel) != tt.wantSelKeys {
				t.Errorf("selections = %v, want %d keys", sel, tt.wantSelKeys)
			}
		})
	}
}

func TestDescribeIsStable(t *testing.T) {
	sel := Selections{"plan": "opus", DefaultKey: "haiku", "review": "sonnet"}
	want := "default=haiku, plan=opus, review=sonnet"
	for range 5 {
		if got := Describe(sel); got != want {
			t.Fatalf("Describe = %q, want %q", got, want)
		}
	}
}
