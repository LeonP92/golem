# internal/orchestrator/config

This module loads and parses the orchestrator server's YAML configuration file. It defines the Config struct (port, database path, session secret, and TLS settings) and the nested TLSConfig struct, and exposes a single Load function that reads a file from disk and unmarshals it into a Config value. It exists as the authoritative source of runtime configuration for the orchestrator server.

## Functions

- Load
- TestLoad

## Types

- TLSConfig
- Config

## Imports

os, gopkg.in/yaml.v3, testing, github.com/leonp92/golem/internal/orchestrator/config
