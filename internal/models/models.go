// Package models defines agent stages, model catalogs, and per-stage
// model resolution.
package models

import (
	"regexp"
	"strings"
)

// Stage is an agent invocation site.
type Stage string

// Tier is an operator-defined label; Catalog.Tiers lists them cheapest first.
type Tier string

const (
	StageBrainstorm    Stage = "brainstorm"
	StagePlan          Stage = "plan"
	StageImplement     Stage = "implement"
	StageRevise        Stage = "revise"
	StageValidate      Stage = "validate"
	StageObserve       Stage = "observe"
	StageReview        Stage = "review"
	StagePRDescription Stage = "pr-description"
	StageGraph         Stage = "graph"
)

// Stages is the closed set of stages, in display order.
var Stages = []Stage{
	StageBrainstorm, StagePlan, StageImplement, StageRevise, StageValidate,
	StageObserve, StageReview, StagePRDescription, StageGraph,
}

func isStage(s string) bool {
	for _, st := range Stages {
		if string(st) == s {
			return true
		}
	}
	return false
}

// DefaultKey is the Selections key holding the ticket-wide model choice.
const DefaultKey = "default"

type Model struct {
	ID    string `json:"id"    yaml:"id"`
	Label string `json:"label" yaml:"label"`
	Tier  Tier   `json:"tier"  yaml:"tier"`
}

// Catalog is a backend's selectable models and per-stage default tiers.
type Catalog struct {
	SupportsSelection bool           `json:"supports_selection" yaml:"supports_selection"`
	Tiers             []Tier         `json:"tiers"              yaml:"tiers"`
	Models            []Model        `json:"models"             yaml:"models"`
	StageDefaults     map[Stage]Tier `json:"stage_defaults"     yaml:"stage_defaults"`
}

// Selections maps a stage name, or DefaultKey, to a model id or a
// "tier:<label>" reference.
type Selections map[string]string

// tierPrefix marks a Selections value naming a tier rather than a model id.
const tierPrefix = "tier:"

// TierRef returns the tier a value names, if it names one.
func TierRef(v string) (Tier, bool) {
	if !strings.HasPrefix(v, tierPrefix) {
		return "", false
	}
	t := Tier(strings.TrimPrefix(v, tierPrefix))
	return t, t != ""
}

// TierValue is the Selections value that names t.
func TierValue(t Tier) string { return tierPrefix + string(t) }

var modelIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ValidModelID reports whether id is safe to pass as a single argv token.
func ValidModelID(id string) bool { return modelIDRE.MatchString(id) }
