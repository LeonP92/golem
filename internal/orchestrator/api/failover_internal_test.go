package api

import (
	"testing"

	"github.com/leonp92/golem/internal/models"
)

func TestTierSelectionsDropsWhatItCannotMap(t *testing.T) {
	src := models.Catalog{Models: []models.Model{{ID: "gpt-large", Tier: "large"}}}
	got := tierSelections(src, models.Selections{"plan": "gpt-large", "review": "tier:small", "implement": "unknown-model"})
	if got["plan"] != "tier:large" || got["review"] != "tier:small" || got["implement"] != "" {
		t.Fatalf("got %v", got)
	}
}
