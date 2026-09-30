package models

import (
	"fmt"
	"sort"
)

// Validate reports the first structural problem in c.
func (c Catalog) Validate() error {
	tiers := map[Tier]bool{}
	for _, t := range c.Tiers {
		tiers[t] = true
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if seen[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = true
		if !ValidModelID(m.ID) {
			return fmt.Errorf("model id %q is not a valid argv token", m.ID)
		}
		if m.Tier != "" && !tiers[m.Tier] {
			return fmt.Errorf("model %q declares tier %q, which is not in tiers %v", m.ID, m.Tier, c.Tiers)
		}
	}
	for stage, tier := range c.StageDefaults {
		if !IsStage(string(stage)) {
			return fmt.Errorf("stage_defaults key %q is not a stage", stage)
		}
		if !tiers[tier] {
			return fmt.Errorf("stage_defaults[%s] = %q, which is not in tiers %v", stage, tier, c.Tiers)
		}
	}
	return nil
}

func (c Catalog) Lookup(id string) (Model, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// Has reports whether every model id in s names a model in c. Tier
// references always resolve, so they are ignored.
func (c Catalog) Has(s Selections) bool {
	for _, v := range s {
		if v == "" {
			continue
		}
		if _, ok := TierRef(v); ok {
			continue
		}
		if _, ok := c.Lookup(v); !ok {
			return false
		}
	}
	return true
}

func (c Catalog) DeclaresTier(t Tier) bool { return c.tierIndex(t) >= 0 }

func (c Catalog) tierIndex(t Tier) int {
	for i, d := range c.Tiers {
		if d == t {
			return i
		}
	}
	return -1
}

// ForTier returns the first model at t, else the nearest cheaper, else
// the nearest larger, else "".
func (c Catalog) ForTier(t Tier) string {
	idx := c.tierIndex(t)
	if idx < 0 {
		return ""
	}
	for i := idx; i >= 0; i-- {
		if id := c.firstAtTier(c.Tiers[i]); id != "" {
			return id
		}
	}
	for i := idx + 1; i < len(c.Tiers); i++ {
		if id := c.firstAtTier(c.Tiers[i]); id != "" {
			return id
		}
	}
	return ""
}

func (c Catalog) firstAtTier(t Tier) string {
	for _, m := range c.Models {
		if m.Tier == t {
			return m.ID
		}
	}
	return ""
}

// ValidateSelections reports the first selection c cannot satisfy.
func (c Catalog) ValidateSelections(s Selections) error {
	for _, k := range sortedKeys(s) {
		v := s[k]
		if v == "" {
			continue
		}
		if k != DefaultKey && !IsStage(k) {
			return fmt.Errorf("unknown stage %q", k)
		}
		if !c.SupportsSelection {
			return fmt.Errorf("backend does not support model selection")
		}
		if t, ok := TierRef(v); ok {
			if !c.DeclaresTier(t) {
				return fmt.Errorf("unknown tier %q", t)
			}
			continue
		}
		if _, ok := c.Lookup(v); !ok {
			return fmt.Errorf("unknown model %q", v)
		}
	}
	return nil
}

func sortedKeys(s Selections) []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Union merges catalogs reported for one backend name.
func Union(cs []Catalog) Catalog {
	out := Catalog{StageDefaults: map[Stage]Tier{}}
	seenTier := map[Tier]bool{}
	seenModel := map[string]bool{}
	for _, c := range cs {
		if c.SupportsSelection {
			out.SupportsSelection = true
		}
		for _, t := range c.Tiers {
			if !seenTier[t] {
				seenTier[t] = true
				out.Tiers = append(out.Tiers, t)
			}
		}
		for _, m := range c.Models {
			if !seenModel[m.ID] {
				seenModel[m.ID] = true
				out.Models = append(out.Models, m)
			}
		}
		for st, t := range c.StageDefaults {
			if _, ok := out.StageDefaults[st]; !ok {
				out.StageDefaults[st] = t
			}
		}
	}
	return out
}
