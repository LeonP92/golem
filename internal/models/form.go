package models

import "net/url"

// FieldPrefix names the form and query fields a selection is submitted in:
// FieldPrefix + DefaultKey, and FieldPrefix + <stage>.
const FieldPrefix = "model_"

// BackendField names the field carrying the backend a selection is for.
const BackendField = FieldPrefix + "backend"

// FromValues reads a selection out of submitted values, dropping empties, and
// returns it with the backend named alongside it.
func FromValues(v url.Values) (map[string]string, string) {
	sel := map[string]string{}
	if got := v.Get(FieldPrefix + DefaultKey); got != "" {
		sel[DefaultKey] = got
	}
	for _, st := range Stages {
		if got := v.Get(FieldPrefix + string(st)); got != "" {
			sel[string(st)] = got
		}
	}
	return sel, v.Get(BackendField)
}
