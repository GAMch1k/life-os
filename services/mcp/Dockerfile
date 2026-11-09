FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/services/mcp
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server
RUN go build -trimpath -ldflags="-s -w" -o /out/processor ./cmd/processor

FROM alpine:3.22 AS service
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]

# The worker needs the Codex CLI or, with LIFE_OS_AI_PROVIDER=claude, the
# Claude Code CLI (its musl build needs libgcc and libstdc++).
# LIFE_OS_AI_PROVIDERS (comma-separated, default codex,claude) chooses which
# CLIs the image installs: codex alone keeps a Codex-only image smaller. The
# entrypoint refuses to start a provider whose CLI is not installed.
# Authentication is never part of this image: Compose mounts the Codex home
# read-only and the entrypoint copies it to tmpfs, or passes the Claude
# subscription token. tzdata lets TZ (the owner's LIFE_OS_TIMEZONE) take effect
# for the runs.
FROM node:22-alpine AS processor
ARG LIFE_OS_AI_PROVIDERS=codex,claude
RUN set -eu; \
    packages=""; libraries=""; \
    for provider in $(echo "$LIFE_OS_AI_PROVIDERS" | tr ',' ' '); do \
      case "$provider" in \
        codex) packages="$packages @openai/codex@0.160.0" ;; \
        claude) packages="$packages @anthropic-ai/claude-code@2.1.319"; libraries="libgcc libstdc++" ;; \
        *) echo "LIFE_OS_AI_PROVIDERS: unknown provider '$provider' (codex, claude)" >&2; exit 1 ;; \
      esac; \
    done; \
    if [ -z "$packages" ]; then echo "LIFE_OS_AI_PROVIDERS names no provider (codex, claude)" >&2; exit 1; fi; \
    apk add --no-cache tzdata $libraries; \
    npm install --global $packages; \
    npm cache clean --force; \
    mkdir -p /workspace; \
    addgroup -S lifeos && adduser -S -G lifeos lifeos
COPY --from=build /out/processor /processor
COPY services/mcp/docker-processor-entrypoint.sh /docker-processor-entrypoint
RUN chmod 0755 /docker-processor-entrypoint
ENV CODEX_HOME=/tmp/lifeos-codex
EXPOSE 8081
ENTRYPOINT ["/docker-processor-entrypoint"]
