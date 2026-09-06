# internal/config

The config module is responsible for loading and validating Golem's configuration from a YAML file. It defines the top-level Config struct and its nested types (GateConfig, PolicyConfig, GraphConfig), exposes a Load function that reads and parses the file while enforcing that the backend field is present, and provides a helper for resolving per-backend ask-and-wait timeout durations. It exists as the single authoritative source of runtime configuration for the rest of the system.

## Functions

- Load
- AskAndWaitTimeoutFor
- TestLoadValidConfig
- TestLoadMissingBackendFails
- TestAskAndWaitTimeoutForUnknownBackendFails

## Types

- GateConfig
- PolicyConfig
- GraphConfig
- Config

## Imports

fmt, os, time, gopkg.in/yaml.v3, path/filepath, testing
