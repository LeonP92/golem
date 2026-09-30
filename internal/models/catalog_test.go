package models

import "testing"

func TestStagesOrder(t *testing.T) {
	want := []Stage{"brainstorm", "plan", "implement", "revise", "validate",
		"observe", "review", "pr-description", "graph"}
	if len(Stages) != len(want) {
		t.Fatalf("Stages has %d entries, want %d", len(Stages), len(want))
	}
	for i, s := range want {
		if Stages[i] != s {
			t.Errorf("Stages[%d] = %q, want %q", i, Stages[i], s)
		}
	}
}

func TestForTier(t *testing.T) {
	onlyB := Catalog{Tiers: []Tier{"a", "b", "c", "d"}, Models: []Model{{ID: "mb", Tier: "b"}}}
	tests := []struct {
		name string
		cat  Catalog
		tier Tier
		want string
	}{
		{"exact", onlyB, "b", "mb"},
		{"falls back to cheaper", onlyB, "d", "mb"},
		{"falls up to larger", onlyB, "a", "mb"},
		{"undeclared tier", onlyB, "z", ""},
		{"no models", Catalog{Tiers: []Tier{"a"}}, "a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cat.ForTier(tt.tier); got != tt.want {
				t.Errorf("ForTier(%q) = %q, want %q", tt.tier, got, tt.want)
			}
		})
	}
}

func TestCatalogValidateSelections(t *testing.T) {
	cat := Catalog{
		SupportsSelection: true,
		Tiers:             []Tier{"small", "large"},
		Models:            []Model{{ID: "haiku", Tier: "small"}, {ID: "opus", Tier: "large"}},
	}
	single := Catalog{Models: []Model{{ID: "only"}}}
	tests := []struct {
		name    string
		cat     Catalog
		sel     Selections
		wantErr string
	}{
		{"unknown stage", cat, Selections{"nope": "opus"}, `unknown stage "nope"`},
		{"unknown model", cat, Selections{"plan": "gpt"}, `unknown model "gpt"`},
		{"unknown tier", cat, Selections{"plan": "tier:huge"}, `unknown tier "huge"`},
		{"no selection support", single, Selections{"plan": "only"}, "backend does not support model selection"},
		{"declared tier passes", cat, Selections{"plan": "tier:large"}, ""},
		{"default key passes", cat, Selections{DefaultKey: "opus"}, ""},
		{"empty value ignored", single, Selections{"plan": ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cat.ValidateSelections(tt.sel)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("want error %q, got nil", tt.wantErr)
			case tt.wantErr != "" && err.Error() != tt.wantErr:
				t.Errorf("error = %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCatalogHas(t *testing.T) {
	cat := Catalog{SupportsSelection: true, Tiers: []Tier{"small"}, Models: []Model{{ID: "haiku", Tier: "small"}}}
	tests := []struct {
		name string
		sel  Selections
		want bool
	}{
		{"empty", Selections{}, true},
		{"tier only, tier not declared", Selections{"plan": "tier:huge"}, true},
		{"known id", Selections{"plan": "haiku"}, true},
		{"absent id", Selections{"plan": "opus"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cat.Has(tt.sel); got != tt.want {
				t.Errorf("Has(%v) = %v, want %v", tt.sel, got, tt.want)
			}
		})
	}
}

func TestTierRef(t *testing.T) {
	tests := []struct {
		in     string
		want   Tier
		wantOK bool
	}{
		{"tier:large", "large", true},
		{"opus", "", false},
		{"tier:", "", false},
	}
	for _, tt := range tests {
		got, ok := TierRef(tt.in)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("TierRef(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestValidModelID(t *testing.T) {
	for _, ok := range []string{"opus", "claude-opus-4-7", "us.anthropic.x:1"} {
		if !ValidModelID(ok) {
			t.Errorf("ValidModelID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "-p", "--model", "a b", "a;b", "a=b"} {
		if ValidModelID(bad) {
			t.Errorf("ValidModelID(%q) = true, want false", bad)
		}
	}
}

func TestUnion(t *testing.T) {
	a := Catalog{
		Tiers:         []Tier{"small", "large"},
		Models:        []Model{{ID: "haiku", Tier: "small"}},
		StageDefaults: map[Stage]Tier{StagePlan: "large"},
	}
	b := Catalog{
		SupportsSelection: true,
		Tiers:             []Tier{"large", "huge"},
		Models:            []Model{{ID: "haiku", Tier: "small"}, {ID: "opus", Tier: "large"}},
		StageDefaults:     map[Stage]Tier{StagePlan: "small", StageReview: "huge"},
	}
	tests := []struct {
		name              string
		in                []Catalog
		wantTiers         []Tier
		wantModels        []string
		wantSelection     bool
		wantPlanDefault   Tier
		wantReviewDefault Tier
	}{
		{name: "nil", in: nil, wantTiers: nil, wantModels: nil},
		{
			name: "two catalogs", in: []Catalog{a, b},
			wantTiers: []Tier{"small", "large", "huge"}, wantModels: []string{"haiku", "opus"},
			wantSelection: true, wantPlanDefault: "large", wantReviewDefault: "huge",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Union(tt.in)
			if len(got.Tiers) != len(tt.wantTiers) {
				t.Fatalf("tiers = %v, want %v", got.Tiers, tt.wantTiers)
			}
			for i, w := range tt.wantTiers {
				if got.Tiers[i] != w {
					t.Errorf("tiers[%d] = %q, want %q", i, got.Tiers[i], w)
				}
			}
			if len(got.Models) != len(tt.wantModels) {
				t.Fatalf("models = %v, want %v", got.Models, tt.wantModels)
			}
			for i, w := range tt.wantModels {
				if got.Models[i].ID != w {
					t.Errorf("models[%d].ID = %q, want %q", i, got.Models[i].ID, w)
				}
			}
			if got.SupportsSelection != tt.wantSelection {
				t.Errorf("SupportsSelection = %v, want %v", got.SupportsSelection, tt.wantSelection)
			}
			if got.StageDefaults[StagePlan] != tt.wantPlanDefault {
				t.Errorf("StageDefaults[plan] = %q, want %q", got.StageDefaults[StagePlan], tt.wantPlanDefault)
			}
			if got.StageDefaults[StageReview] != tt.wantReviewDefault {
				t.Errorf("StageDefaults[review] = %q, want %q", got.StageDefaults[StageReview], tt.wantReviewDefault)
			}
		})
	}
}

func TestCatalogValidate(t *testing.T) {
	tests := []struct {
		name    string
		cat     Catalog
		wantErr bool
	}{
		{"valid", Catalog{Tiers: []Tier{"small"}, Models: []Model{{ID: "haiku", Tier: "small"}},
			StageDefaults: map[Stage]Tier{StagePlan: "small"}}, false},
		{"duplicate id", Catalog{Models: []Model{{ID: "a"}, {ID: "a"}}}, true},
		{"invalid id token", Catalog{Models: []Model{{ID: "--model"}}}, true},
		{"model tier not declared", Catalog{Models: []Model{{ID: "a", Tier: "huge"}}}, true},
		{"stage_defaults key not a stage", Catalog{Tiers: []Tier{"small"},
			StageDefaults: map[Stage]Tier{"nope": "small"}}, true},
		{"stage_defaults value not a tier", Catalog{Tiers: []Tier{"small"},
			StageDefaults: map[Stage]Tier{StagePlan: "huge"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cat.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
