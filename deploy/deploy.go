// Package deploy holds the shipped deployment manifests — the orchestrator
// and shem configs in this directory, and the docker-compose.yml and
// .env.example one level up — together with the tests that keep them honest.
//
// It carries no production code on purpose. The manifests are the project's
// documented install path, and nothing in the Go build depends on them, so
// without a test here a manifest can silently stop delivering something the
// binaries require (see compose_test.go) while every other package stays
// green.
package deploy
