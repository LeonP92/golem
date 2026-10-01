package ui

import (
	"net/http"
	"strings"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// option is one choice in a model select.
type option struct{ Value, Label string }

// optionGroup is one labelled group of options.
type optionGroup struct {
	Label   string
	Options []option
}

// modelSelect is one select: its form field, its label, the text of its empty
// option, the value currently chosen, and its options.
type modelSelect struct {
	Field, Label, Empty, Chosen string
	Groups                      []optionGroup
}

// modelPickerData is the view-model for the model selects.
type modelPickerData struct {
	Backends []db.BackendCatalog
	Backend  string // the selected backend, "" when none is reported
	// Unreported is set when Backend is the ticket's own and no shem reports
	// it: no selects render, so nothing on the page can rewrite its selections.
	Unreported bool
	Catalog    models.Catalog // the selected backend's catalog
	Default    modelSelect
	Stages     []modelSelect
}

// modelPicker builds the selects for one backend; an empty backend picks the
// first reported one, and an unreported one is kept as is.
func modelPicker(fleet db.Fleet, backend string, sel map[string]string) modelPickerData {
	out := modelPickerData{Backends: fleet.Backends()}
	if len(out.Backends) == 0 {
		return out
	}
	out.Backend = backend
	cat, ok := fleet.Catalog(backend)
	if !ok && backend != "" {
		out.Unreported = true
		return out
	}
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
		Groups: modelGroups(out.Backends, out.Backend, cat, sel[models.DefaultKey]),
	}
	for _, st := range models.Stages {
		empty := "Default (vendor)"
		if id := fallback[st]; id != "" {
			empty = "Default (" + id + ")"
		}
		out.Stages = append(out.Stages, modelSelect{
			Field:  models.FieldPrefix + string(st),
			Label:  string(st),
			Empty:  empty,
			Chosen: sel[string(st)],
			Groups: modelGroups(out.Backends, out.Backend, cat, sel[string(st)]),
		})
	}
	return out
}

// singleSelect is one standalone model select outside the picker panel, with
// the hidden model_backend the action needs alongside it.
type singleSelect struct {
	Select  modelSelect
	Backend string
	Show    bool
}

// singleModelSelect builds one standalone select from the picker. Show is
// false when no catalog is reported or the backend runs a single model, so the
// control renders no model field at all.
func singleModelSelect(p modelPickerData, field, label string) singleSelect {
	return singleSelect{
		Select: modelSelect{Field: field, Label: label, Empty: "unchanged",
			Groups: modelGroups(p.Backends, p.Backend, p.Catalog, "")},
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

// ticketModels is what each stage runs on: resolved against the assigned
// shem's own catalog once claimed, else the bound backend's, else the first
// reported backend's.
func ticketModels(fleet db.Fleet, t db.Ticket, assigned *db.Shem) map[models.Stage]string {
	sel := t.ModelSelections()
	if assigned != nil {
		if t.ModelBackend != "" && t.ModelBackend != assigned.Backend {
			sel = nil
		}
		out, _ := models.Resolve(assigned.ModelCatalog(), sel)
		return out
	}
	cat, ok := fleet.Catalog(t.ModelBackend)
	if !ok && t.ModelBackend == "" {
		if b := fleet.Backends(); len(b) > 0 {
			cat = b[0].Catalog
		}
	}
	out, _ := models.Resolve(cat, sel)
	return out
}

// stageModel is one row of a ticket's resolved models.
type stageModel struct{ Stage, Model string }

// inStageOrder lists m in models.Stages order.
func inStageOrder(m map[models.Stage]string) []stageModel {
	out := make([]stageModel, 0, len(models.Stages))
	for _, st := range models.Stages {
		out = append(out, stageModel{Stage: string(st), Model: m[st]})
	}
	return out
}

// modelGroups lists one select's choices: the backend's models and, when the
// fleet runs several backends, sizes that resolve on any of them. A size
// already chosen stays listed, so saving the form cannot drop it.
func modelGroups(fleet []db.BackendCatalog, backend string, cat models.Catalog, chosen string) []optionGroup {
	multi := len(fleet) > 1
	chosenTier, chosenIsTier := models.TierRef(chosen)
	var sizes []option
	for _, t := range cat.Tiers {
		if multi || (chosenIsTier && t == chosenTier) {
			sizes = append(sizes, option{Value: models.TierValue(t), Label: sizeLabel(fleet, t)})
		}
	}
	var groups []optionGroup
	if len(sizes) > 0 {
		groups = append(groups, optionGroup{Label: "Any shem, by size", Options: sizes})
	}
	label := "Models"
	if multi {
		label = "Only " + backend + " shems"
	}
	group := optionGroup{Label: label}
	for _, m := range cat.Models {
		group.Options = append(group.Options, option{Value: m.ID, Label: m.Label})
	}
	return append(groups, group)
}

// sizeLabel names a tier and the model it resolves to on each backend.
func sizeLabel(fleet []db.BackendCatalog, t models.Tier) string {
	var on []string
	for _, b := range fleet {
		if id := b.Catalog.ForTier(t); id != "" {
			on = append(on, id+" on "+b.Name)
		}
	}
	if len(on) == 0 {
		return string(t)
	}
	return string(t) + " — " + strings.Join(on, ", ")
}
