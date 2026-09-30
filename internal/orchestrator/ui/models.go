package ui

import (
	"net/http"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// modelSelect is one select: its form field, its label, the text of its empty
// option, and the value currently chosen.
type modelSelect struct {
	Field, Label, Empty, Chosen string
}

// modelPickerData is the view-model for the model selects.
type modelPickerData struct {
	Backends []db.BackendCatalog
	Backend  string         // the selected backend, "" when none is reported
	Catalog  models.Catalog // the selected backend's catalog
	Default  modelSelect
	Stages   []modelSelect
}

// modelPicker builds the selects for one backend; an empty backend picks the
// first reported one.
func modelPicker(fleet db.Fleet, backend string, sel map[string]string) modelPickerData {
	out := modelPickerData{Backends: fleet.Backends()}
	if len(out.Backends) == 0 {
		return out
	}
	out.Backend = backend
	cat, ok := fleet.Catalog(backend)
	if !ok {
		out.Backend = out.Backends[0].Name
		cat = out.Backends[0].Catalog
	}
	out.Catalog = cat

	// Named from the selected backend's own catalog, so the model_backend
	// field and the options can never name different backends.
	fallback, _ := models.Resolve(cat, nil)
	out.Default = modelSelect{
		Field:  models.FieldPrefix + models.DefaultKey,
		Empty:  "Per-stage defaults",
		Chosen: sel[models.DefaultKey],
	}
	for _, st := range models.Stages {
		empty := "default"
		if id := fallback[st]; id != "" {
			empty = id
		}
		out.Stages = append(out.Stages, modelSelect{
			Field:  models.FieldPrefix + string(st),
			Label:  string(st),
			Empty:  empty,
			Chosen: sel[string(st)],
		})
	}
	return out
}

// singleSelect is one standalone model select outside the picker panel, with
// the hidden model_backend the action needs alongside it.
type singleSelect struct {
	Select  modelSelect
	Catalog models.Catalog
	Backend string
	Show    bool
}

// singleModelSelect builds one standalone select from the picker. Show is
// false when no catalog is reported or the backend runs a single model, so the
// control renders no model field at all.
func singleModelSelect(p modelPickerData, field, label string) singleSelect {
	return singleSelect{
		Select:  modelSelect{Field: field, Label: label, Empty: "unchanged"},
		Catalog: p.Catalog,
		Backend: p.Backend,
		Show:    len(p.Backends) > 0 && p.Catalog.SupportsSelection,
	}
}

// modelSelects re-renders the model selects for the chosen backend.
func (h *Handlers) modelSelects(w http.ResponseWriter, r *http.Request) {
	fleet, err := db.LoadFleet(h.DB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sel, backend := models.FromValues(r.URL.Query())
	picker := modelPicker(fleet, backend, sel)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpls["ticket_new"].ExecuteTemplate(w, "model_selects", picker); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
