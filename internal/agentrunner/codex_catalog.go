package agentrunner

import (
	_ "embed"

	"github.com/leonp92/golem/internal/models"
)

//go:embed codex_catalog.yaml
var codexCatalogYAML []byte

var codexCatalog = mustParseCatalog(codexCatalogYAML)

func (c Codex) DefaultCatalog() models.Catalog { return codexCatalog }
