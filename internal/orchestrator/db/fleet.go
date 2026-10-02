package db

import (
	"sort"

	"github.com/leonp92/golem/internal/models"
	"gorm.io/gorm"
)

// BackendCatalog is one backend name and its union catalog.
type BackendCatalog struct {
	Name    string         `json:"name"`
	Catalog models.Catalog `json:"catalog"`
}

// Fleet is every registered shem's reported catalog, loaded once.
type Fleet struct {
	order     []string
	byBackend map[string][]models.Catalog
}

// LoadFleet reads every registered shem's reported catalog.
func LoadFleet(gdb *gorm.DB) (Fleet, error) {
	var shems []Shem
	// name asc keeps the union deterministic. Offline rows count: a reported
	// catalog describes a machine, not its liveness.
	if err := gdb.Order("name asc").Find(&shems).Error; err != nil {
		return Fleet{}, err
	}
	f := Fleet{byBackend: map[string][]models.Catalog{}}
	for _, s := range shems {
		if s.Backend == "" {
			continue
		}
		cat := s.ModelCatalog()
		if len(cat.Models) == 0 {
			continue
		}
		if _, ok := f.byBackend[s.Backend]; !ok {
			f.order = append(f.order, s.Backend)
		}
		f.byBackend[s.Backend] = append(f.byBackend[s.Backend], cat)
	}
	sort.Strings(f.order)
	return f, nil
}

// Backends returns each backend's union catalog, by name.
func (f Fleet) Backends() []BackendCatalog {
	out := make([]BackendCatalog, 0, len(f.order))
	for _, n := range f.order {
		out = append(out, BackendCatalog{Name: n, Catalog: models.Union(f.byBackend[n])})
	}
	return out
}

// Catalog returns one backend's union catalog and whether any shem reports it.
func (f Fleet) Catalog(name string) (models.Catalog, bool) {
	cats, ok := f.byBackend[name]
	if !ok {
		return models.Catalog{}, false
	}
	return models.Union(cats), true
}

// Catalogs returns every backend's per-shem catalogs keyed by name.
func (f Fleet) Catalogs() map[string][]models.Catalog {
	return f.byBackend
}

// WaitingFor returns why no shem can claim t, else "".
func (f Fleet) WaitingFor(t Ticket) string {
	if t.ModelBackend == "" || t.AssignedShem != nil || t.Phase != "unassigned" {
		return ""
	}
	cats := f.byBackend[t.ModelBackend]
	if len(cats) == 0 {
		return "waiting for a " + t.ModelBackend + " shem"
	}
	sel := t.ModelSelections()
	for _, c := range cats {
		if c.Has(sel) {
			return ""
		}
	}
	return "waiting for a " + t.ModelBackend + " shem whose catalog has " + models.Describe(sel)
}
