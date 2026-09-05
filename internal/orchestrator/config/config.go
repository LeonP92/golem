package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// TLSConfig holds TLS certificate and key paths.
type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Config holds all orchestrator server configuration.
type Config struct {
	Port          int       `yaml:"port"`
	DBPath        string    `yaml:"db_path"`
	SessionSecret string    `yaml:"session_secret"`
	TLS           TLSConfig `yaml:"tls"`
}

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	return &cfg, yaml.Unmarshal(data, &cfg)
}
