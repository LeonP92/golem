# internal/shem/config

This module loads and validates shem-node configuration from a YAML file (shem.yaml). It defines the Config struct holding orchestrator URL, API key, node name, managed repository list, and operational flags (NoPush, MaxConcurrent), as well as the RepoConfig struct for individual repository entries. The Load function reads the file, unmarshals YAML, normalizes repository remote URLs via urlnorm, and allows environment variables (GOLEM_SHEM_API_KEY, GOLEM_SHEM_NAME) to override file-level values for Docker-friendly deployments.

## Functions

- Load
- TestLoad_NormalizesRepos
- TestLoad_Fields
- TestLoad_MissingFile

## Types

- RepoConfig
- Config

## Imports

os, github.com/leonp92/golem/internal/orchestrator/urlnorm, gopkg.in/yaml.v3, testing, github.com/leonp92/golem/internal/shem/config
