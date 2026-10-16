package agentrunner

import (
	_ "embed"

	"github.com/leonp92/golem/internal/models"
)

//go:embed opencode_catalog.yaml
var opencodeCatalogYAML []byte

var opencodeCatalog = mustParseCatalog(opencodeCatalogYAML)

func (o OpenCode) DefaultCatalog() models.Catalog { return opencodeCatalog }
