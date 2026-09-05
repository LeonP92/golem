# syntax=docker/dockerfile:1
# Multi-stage build producing three binaries: golem, golem-orchestrator, golem-shem.
# CGO is disabled — the pure-Go SQLite driver (glebarez/sqlite) is used.

FROM golang:1.27-alpine AS builder
# build-base provides musl-gcc (C toolchain) required for tree-sitter CGO bindings.
# libstdc++ is needed by the TypeScript grammar (C++ runtime).
RUN apk add --no-cache \
    build-base \
    libstdc++ \
    git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/golem              ./cmd/golem
RUN CGO_ENABLED=0 go build -o /out/golem-orchestrator  ./cmd/orchestrator
# golem-shem uses tree-sitter (CGO) — build statically with musl-gcc.
RUN CGO_ENABLED=1 \
    CC=musl-gcc \
    go build \
      -tags netgo \
      -ldflags '-linkmode=external -extldflags=-static' \
      -o /out/golem-shem \
      ./cmd/shem

# ── orchestrator runtime ───────────────────────────────────────────────────────
FROM alpine:3.21 AS orchestrator
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/golem-orchestrator /usr/local/bin/golem-orchestrator
EXPOSE 8080
ENTRYPOINT ["golem-orchestrator"]
CMD ["/etc/golem/orchestrator.yaml"]

# ── shem runtime ──────────────────────────────────────────────────────────────
# The shem needs git, the golem CLI, and the Claude Code CLI (claude --print).
# ANTHROPIC_API_KEY is required at runtime — no interactive login needed when
# the key is set; claude --print uses it directly.
FROM node:22-alpine AS shem
# Common runtimes and build tools for polyglot ticket support.
# go is omitted (heavy); add it to a custom image if Go tickets are needed.
RUN apk add --no-cache \
    ca-certificates git openssh-client \
    bash python3 py3-pip \
    go \
    make curl jq
ENV SHELL=/bin/bash
RUN npm install -g @anthropic-ai/claude-code
COPY --from=builder /out/golem      /usr/local/bin/golem
COPY --from=builder /out/golem-shem /usr/local/bin/golem-shem
COPY deploy/shem-entrypoint.sh /usr/local/bin/shem-entrypoint.sh
RUN chmod +x /usr/local/bin/shem-entrypoint.sh
ENTRYPOINT ["shem-entrypoint.sh"]
CMD ["-config", "/etc/golem/shem.yaml"]
