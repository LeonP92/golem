package models

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ValidateSelections checks raw against the named backend's catalog and
// returns the selections to store and the backend to bind them to. A
// tier-only selection binds no backend.
func ValidateSelections(catalogs map[string]Catalog, backend string, raw map[string]string) (Selections, string, error) {
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
			if catalogs[n].ValidateSelections(sel) == nil {
				return sel, "", nil
			}
		}
		return nil, "", fmt.Errorf("no registered shem declares %s", Describe(sel))
	}
	cat, ok := catalogs[backend]
	if !ok {
		return nil, "", fmt.Errorf("no registered shem reports backend %q", backend)
	}
	if err := cat.ValidateSelections(sel); err != nil {
		return nil, "", err
	}
	if !concrete {
		// Nothing here is vendor-specific, so leave the ticket claimable
		// by any backend.
		return sel, "", nil
	}
	return sel, backend, nil
}

// Describe renders sel as "default=opus, plan=sonnet".
func Describe(sel Selections) string {
	parts := make([]string, 0, len(sel))
	for _, k := range sortedKeys(sel) {
		parts = append(parts, k+"="+sel[k])
	}
	return strings.Join(parts, ", ")
}
