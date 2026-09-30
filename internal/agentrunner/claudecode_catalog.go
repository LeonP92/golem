package agentrunner

import (
	"bytes"
	_ "embed"

	"github.com/leonp92/golem/internal/models"
	"gopkg.in/yaml.v3"
)

//go:embed claudecode_catalog.yaml
var claudeCodeCatalogYAML []byte

var claudeCodeCatalog = mustParseCatalog(claudeCodeCatalogYAML)

// mustParseCatalog decodes an embedded catalog; a bad one is a build defect.
func mustParseCatalog(data []byte) models.Catalog {
	var c models.Catalog
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		panic("agentrunner: embedded catalog: " + err.Error())
	}
	if err := c.Validate(); err != nil {
		panic("agentrunner: embedded catalog: " + err.Error())
	}
	return c
}

func (c ClaudeCode) DefaultCatalog() models.Catalog { return claudeCodeCatalog }
