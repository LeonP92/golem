package models

import (
	"strings"
	"testing"
)

func testCatalog() Catalog {
	return Catalog{
		SupportsSelection: true,
		Tiers:             []Tier{"small", "medium", "large"},
		Models: []Model{
			{ID: "haiku", Tier: "small"},
			{ID: "sonnet", Tier: "medium"},
			{ID: "opus", Tier: "large"},
		},
		StageDefaults: map[Stage]Tier{
			StageBrainstorm: "large", StagePlan: "large", StageImplement: "medium",
			StageRevise: "medium", StageValidate: "small", StageObserve: "small",
			StageReview: "medium", StagePRDescription: "small", StageGraph: "small",
		},
	}
}

func TestResolve(t *testing.T) {
	cat := testCatalog()
	tests := []struct {
		name        string
		sel         Selections
		wantStage   Stage
		wantModel   string
		wantDropped []string
	}{
		{"no selection uses the stage default", nil, StageBrainstorm, "opus", nil},
		{"stage override wins", Selections{"brainstorm": "haiku", DefaultKey: "sonnet"}, StageBrainstorm, "haiku", nil},
		{"default key is the fallback", Selections{DefaultKey: "haiku"}, StagePlan, "haiku", nil},
		{"unknown stage value drops once", Selections{"brainstorm": "nope"}, StageBrainstorm, "opus", []string{"brainstorm=nope"}},
		{"unknown default value drops once", Selections{DefaultKey: "nope"}, StagePlan, "opus", []string{"default=nope"}},
		{"declared tier resolves", Selections{"brainstorm": "tier:small"}, StageBrainstorm, "haiku", nil},
		{"undeclared tier drops", Selections{"brainstorm": "tier:huge"}, StageBrainstorm, "opus", []string{"brainstorm=tier:huge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := Resolve(cat, tt.sel)
			if len(got) != len(Stages) {
				t.Fatalf("resolved %d stages, want %d", len(got), len(Stages))
			}
			if got[tt.wantStage] != tt.wantModel {
				t.Errorf("%s = %q, want %q", tt.wantStage, got[tt.wantStage], tt.wantModel)
			}
			if strings.Join(dropped, ",") != strings.Join(tt.wantDropped, ",") {
				t.Errorf("dropped = %v, want %v", dropped, tt.wantDropped)
			}
		})
	}
}

func TestResolveWithoutSelectionSupport(t *testing.T) {
	cat := Catalog{Models: []Model{{ID: "only"}}}
	got, dropped := Resolve(cat, Selections{DefaultKey: "only", "plan": "other"})
	if len(got) != len(Stages) {
		t.Fatalf("resolved %d stages, want %d", len(got), len(Stages))
	}
	for st, id := range got {
		if id != "" {
			t.Errorf("%s = %q, want the vendor default", st, id)
		}
	}
	if dropped != nil {
		t.Errorf("dropped = %v, want nothing", dropped)
	}
}

func TestResolveTierIsPerCatalog(t *testing.T) {
	a := Catalog{SupportsSelection: true, Tiers: []Tier{"large"}, Models: []Model{{ID: "opus", Tier: "large"}}}
	b := Catalog{SupportsSelection: true, Tiers: []Tier{"large"}, Models: []Model{{ID: "gpt-big", Tier: "large"}}}
	sel := Selections{DefaultKey: "tier:large"}
	gotA, _ := Resolve(a, sel)
	gotB, _ := Resolve(b, sel)
	if gotA[StagePlan] != "opus" || gotB[StagePlan] != "gpt-big" {
		t.Errorf("plan resolved to %q and %q, want opus and gpt-big", gotA[StagePlan], gotB[StagePlan])
	}
}
