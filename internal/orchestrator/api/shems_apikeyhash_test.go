package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// TestShemJSONOmitsAPIKeyHash: GET /api/shems serializes db.Shem straight to
// the dashboard. Verified against HEAD 51d9526:
// [{"ID":1,"Name":"shemA","APIKeyHash":"$2a$10$bFAAx…"}]. Session-gated and
// bcrypt over 32 random bytes, so not exploitable — but there is no reason
// to ship credential material to a browser at all.
func TestShemJSONOmitsAPIKeyHash(t *testing.T) {
	shem := db.Shem{ID: 1, Name: "shemA",
		APIKeyHash: "$2a$10$bFAAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		Repos:      "[]", Status: "online"}

	encoded, err := json.Marshal(shem)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	got := string(encoded)
	if strings.Contains(got, "APIKeyHash") {
		t.Errorf("serialized shem still carries the APIKeyHash field: %s", got)
	}
	if strings.Contains(got, shem.APIKeyHash) {
		t.Errorf("serialized shem leaks the api key hash value: %s", got)
	}
	// The fields the dashboard actually needs must survive.
	for _, want := range []string{`"Name":"shemA"`, `"Status":"online"`} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized shem lost %s: %s", want, got)
		}
	}
}
