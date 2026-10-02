package models

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ValidateSelections checks raw against the named backend's shem catalogs,
// one of which must declare all of it, and returns the selections to store
// and the backend to bind them to. A tier-only selection binds no backend.
func ValidateSelections(catalogs map[string][]Catalog, backend string, raw map[string]string) (Selections, string, error) {
	sel := Selections{}
	concrete := false
	for k, v := range raw {
		if v == "" {
			continue
		}
		sel[k] = v
		if _, isTier := TierRef(v); !isTier {
			concrete = true
		}
	}
	if len(sel) == 0 {
		return sel, "", nil
	}
	if backend == "" {
		if concrete {
			return nil, "", errors.New("backend is required when a model is selected")
		}
		names := make([]string, 0, len(catalogs))
		for n := range catalogs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if validateAny(catalogs[n], sel) == nil {
				return sel, "", nil
			}
		}
		return nil, "", fmt.Errorf("no registered shem declares %s", Describe(sel))
	}
	cats, ok := catalogs[backend]
	if !ok {
		return nil, "", fmt.Errorf("no registered shem reports backend %q", backend)
	}
	if err := validateAny(cats, sel); err != nil {
		return nil, "", err
	}
	if !concrete {
		// Nothing here is vendor-specific, so leave the ticket claimable
		// by any backend.
		return sel, "", nil
	}
	return sel, backend, nil
}

// validateAny accepts sel if one catalog declares all of it, since a claim
// needs a single shem with every selected id.
func validateAny(cats []Catalog, sel Selections) error {
	for _, c := range cats {
		if c.ValidateSelections(sel) == nil {
			return nil
		}
	}
	if err := Union(cats).ValidateSelections(sel); err != nil {
		return err
	}
	return fmt.Errorf("no single shem declares %s", Describe(sel))
}

// Describe renders sel as "default=opus, plan=sonnet".
func Describe(sel Selections) string {
	parts := make([]string, 0, len(sel))
	for _, k := range sortedKeys(sel) {
		parts = append(parts, k+"="+sel[k])
	}
	return strings.Join(parts, ", ")
}
