# syntax=docker/dockerfile:1
# Base images are pinned by digest (Dependabot proposes updates, see .github/dependabot.yml); the tag is
# kept for readability.
# Multi-stage build producing three binaries: golem, golem-orchestrator, golem-shem.
# golem and golem-shem use tree-sitter CGO bindings; all three are built statically
# with musl-gcc. golem-orchestrator is pure-Go (glebarez/sqlite) and needs no CGO.

# The shem image carries the official Go toolchain, copied from here: Debian's own golang-go lags (Go 1.19 on bookworm), which is
# out of support and cannot build this module (go 1.27) or switch toolchains.
FROM golang:1.27-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS go-toolchain

FROM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder
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
# The release workflow passes the tag so `golem version` in the image matches
# the binaries attached to the same release; local builds stay "dev".
ARG VERSION=dev
# golem includes graph commands that use tree-sitter (CGO) — build statically.
RUN CGO_ENABLED=1 \
    CC=gcc \
    go build \
      -tags netgo \
      -ldflags "-linkmode=external -extldflags=-static -X main.version=$VERSION" \
      -o /out/golem \
      ./cmd/golem
RUN CGO_ENABLED=0 go build -o /out/golem-orchestrator  ./cmd/orchestrator
# golem-shem uses tree-sitter (CGO) — build statically with musl-gcc.
RUN CGO_ENABLED=1 \
    CC=gcc \
    go build \
      -tags netgo \
      -ldflags '-linkmode=external -extldflags=-static' \
      -o /out/golem-shem \
      ./cmd/shem

# ── orchestrator runtime ───────────────────────────────────────────────────────
FROM alpine:3.21@sha256:ee16d92f96db08d9d2d07da48555a27f3a69d8f0759021dbc601195ed0d2b5bd AS orchestrator
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/golem-orchestrator /usr/local/bin/golem-orchestrator
EXPOSE 8080
ENTRYPOINT ["golem-orchestrator"]
CMD ["/etc/golem/orchestrator.yaml"]

# ── shem runtime ──────────────────────────────────────────────────────────────
# The shem needs git, the golem CLI, and the agent CLI for each backend the
# image supports. GOLEM_BACKENDS is a space-separated list of adapter names
# (claude-code, codex, opencode); the default builds the claude-code-only
# image the stack has always shipped, and docker-compose.yml defaults it to
# the selected GOLEM_BACKEND. The release workflow overrides it to install
# all three, so the published image serves any backend.
#
# Everything else is runtime configuration passed through the environment:
# GOLEM_BACKEND (required, no default) selects the backend, and the auth
# variables below carry its credentials. The entrypoint refuses to start when
# GOLEM_BACKEND is missing or unknown, its CLI is absent, or no credential
# for it exists — see deploy/shem-entrypoint.sh. For claude-code: one of
# ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, or a mounted CLAUDE_HOME
# session. See .env.example for codex and opencode.
# Debian, not Alpine: Playwright's browser builds need glibc and musl is
# unsupported (https://playwright.dev/docs/docker), and the baked-in Chromium
# below is what lets the agent test UI headlessly. node:22-trixie-slim
# keeps the same Node toolchain the Alpine image carried.
FROM node:25-trixie-slim@sha256:aabbe39553d15ede8a97cc60c9e1a97034ff772afcf696ea42b94e7f5f2ec71b AS shem
# All three CLIs by default: the image is one runtime-configured image,
# not one build per backend. GOLEM_BACKEND picks among them at startup.
ARG GOLEM_BACKENDS="claude-code codex opencode"
# Agent CLI versions are pinned so a build is reproducible and a compromised or broken new release of one cannot
# enter an image unreviewed. Bump them deliberately (and test a ticket) when you want a newer CLI.
ARG CLAUDE_CODE_VERSION=2.1.312
ARG CODEX_VERSION=0.165.0
ARG OPENCODE_VERSION=1.19.5
# Common runtimes and build tools for polyglot ticket support.
# The base is Debian trixie, not bookworm: golem requires git 2.40 or newer
# (check-attr --source, internal/prereq) and bookworm ships 2.39, which stops the
# shem at startup. The build fails if git is older, rather than shipping a shem
# that cannot start.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git openssh-client \
    bash python3 python3-pip \
    make curl jq \
    && rm -rf /var/lib/apt/lists/* \
    && v="$(git version | sed 's/^git version //')" \
    && [ "$(printf '2.40\n%s\n' "$v" | sort -V | head -1)" = "2.40" ] \
    || { echo "git is older than 2.40" >&2; exit 1; }
ENV SHELL=/bin/bash
COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
# The agent runs as this account, not as root.
#
# golem-shem itself stays root: it holds GOLEM_GITHUB_TOKEN, which the push
# needs. Environment filtering (internal/agentenv) keeps that token out of the
# agent's own environment, but an agent running as root just reads it out of
# /proc/1/environ instead — /proc/1/environ is mode 0400 root-owned, which is
# no obstacle to root in the same container. The filtering only means
# something once the agent is a different, unprivileged user.
RUN groupadd -g 10001 golem-agent \
    && useradd -u 10001 -g golem-agent -m -d /home/golem-agent -s /bin/bash golem-agent
ENV GOLEM_AGENT_USER=golem-agent
# Install the agent CLI for each requested backend. Unknown names fail the
# build rather than producing an image that misses a CLI it claims to have.
RUN for backend in $GOLEM_BACKENDS; do \
      case "$backend" in \
        claude-code) npm install -g @anthropic-ai/claude-code@$CLAUDE_CODE_VERSION ;; \
        codex)       npm install -g @openai/codex@$CODEX_VERSION ;; \
        opencode)    npm install -g opencode-ai@$OPENCODE_VERSION ;; \
        *) echo "unknown GOLEM_BACKENDS entry: $backend" >&2; exit 1 ;; \
      esac; \
    done
# Playwright + Chromium, baked in at build time so the agent can test UI
# headlessly with no download at ticket time. The npm package and its browser
# builds are version-locked to each other, so both are pinned to the same
# version here.
#
# How a ticket uses it (all verified against this layout):
#   - the repo pins @playwright/test  -> `npx playwright test` uses it and
#     finds the matching baked browsers via PLAYWRIGHT_BROWSERS_PATH;
#   - the repo has no playwright dep  -> the global `playwright` CLI and
#     `require("playwright")` work through NODE_PATH (bare `npx playwright`
#     ignores global installs and would download a package instead — do not
#     use it as the no-dependency path);
#   - the repo pins ANOTHER version   -> its browsers are not baked; set
#     PLAYWRIGHT_BROWSERS_PATH to a writable directory (or unset it) and run
#     `npx playwright install chromium` for that run.
# /ms-playwright is read-only for everyone: the browsers are shared
# executables, and the agent runs as golem-agent — it must not be able to
# replace them. Playwright launches Chromium with its sandbox off by
# default, so the container needs no extra capabilities — but it does need
# the larger /dev/shm the compose file grants.
ARG PLAYWRIGHT_VERSION=1.64.1
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    NODE_PATH=/usr/local/lib/node_modules
RUN npm install -g playwright@$PLAYWRIGHT_VERSION @playwright/test@$PLAYWRIGHT_VERSION \
    && apt-get update && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* \
    && chmod -R a-w,a+rX /ms-playwright
COPY --from=builder /out/golem      /usr/local/bin/golem
COPY --from=builder /out/golem-shem /usr/local/bin/golem-shem
COPY deploy/shem-entrypoint.sh /usr/local/bin/shem-entrypoint.sh
RUN sed -i 's/\r//' /usr/local/bin/shem-entrypoint.sh && chmod +x /usr/local/bin/shem-entrypoint.sh
ENTRYPOINT ["shem-entrypoint.sh"]
CMD ["-config", "/etc/golem/shem.yaml"]
