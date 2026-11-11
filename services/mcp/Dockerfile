FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY . .
WORKDIR /src/services/mcp
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server
RUN go build -trimpath -ldflags="-s -w" -o /out/processor ./cmd/processor

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS service
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# The base image is pinned by digest and moves only when Dependabot bumps it;
# Alpine's security fixes land in between, and the release's Trivy gate fails
# on a HIGH or CRITICAL vulnerability that has a fix: the build upgrades the
# packages (SEC-IMG).
RUN apk upgrade --no-cache \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]

# Claude Code, when LIFE_OS_AI_PROVIDERS names claude (a source build only:
# the published images never contain it). services/mcp/claude-code/install.sh
# installs the official npm package at the version and integrity its
# package-lock.json pins, and only its native binary goes on to the processor.
# A release install adds the same binary on the owner's server with
# ./setup.sh claude (services/mcp/claude-code/Dockerfile). Without claude, /out
# stays empty.
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS claude-code
ARG LIFE_OS_AI_PROVIDERS=codex
COPY services/mcp/claude-code/package.json services/mcp/claude-code/package-lock.json services/mcp/claude-code/install.sh /opt/claude-code/
RUN set -eu; mkdir -p /out; \
    for provider in $(echo "$LIFE_OS_AI_PROVIDERS" | tr ',' ' '); do \
      if [ "$provider" = claude ]; then sh /opt/claude-code/install.sh /out; fi; \
    done

# The worker needs the Codex CLI or, with LIFE_OS_AI_PROVIDER=claude, the
# Claude Code CLI. LIFE_OS_AI_PROVIDERS (comma-separated: codex, the default,
# claude, or both) chooses which CLIs the image has; the release builds the
# default, so its image has Codex alone. The entrypoint refuses to start a
# provider whose CLI is not installed.
# Authentication is never part of this image: Compose mounts the Codex home
# read-only and the entrypoint copies it to tmpfs, or passes Claude Code's
# token or API key. tzdata lets TZ (the owner's LIFE_OS_TIMEZONE) take effect
# for the runs.
# npm only installs Codex, whose launcher needs node alone. npm, npx,
# corepack, and yarn are then removed, as nothing runs them and their bundled
# packages were all of the image's HIGH findings (SEC-AUDIT). Each CLI's
# --version fails the build if it needed one of them. The base image is
# pinned by digest and moves only when Dependabot bumps it; Alpine's security
# fixes land in between, and the release's Trivy gate fails on a HIGH or
# CRITICAL vulnerability that has a fix: the build upgrades the packages
# (SEC-IMG).
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS processor
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
ARG LIFE_OS_AI_PROVIDERS=codex
RUN set -eu; \
    packages=""; clis=""; \
    for provider in $(echo "$LIFE_OS_AI_PROVIDERS" | tr ',' ' '); do \
      case "$provider" in \
        codex) packages="$packages @openai/codex@0.160.0"; clis="$clis codex" ;; \
        claude) clis="$clis claude" ;; \
        *) echo "LIFE_OS_AI_PROVIDERS: unknown provider '$provider' (codex, claude)" >&2; exit 1 ;; \
      esac; \
    done; \
    if [ -z "$clis" ]; then echo "LIFE_OS_AI_PROVIDERS names no provider (codex, claude)" >&2; exit 1; fi; \
    apk upgrade --no-cache; \
    apk add --no-cache tzdata; \
    if [ -n "$packages" ]; then npm install --global $packages; fi; \
    rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
        /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
        /opt/yarn-* /usr/local/bin/yarn /usr/local/bin/yarnpkg /root/.npm; \
    mkdir -p /workspace; \
    addgroup -S lifeos && adduser -S -G lifeos lifeos
# Claude Code's binary, when the claude-code stage installed it.
COPY --from=claude-code /out/ /usr/local/bin/
RUN set -eu; \
    for provider in $(echo "$LIFE_OS_AI_PROVIDERS" | tr ',' ' '); do \
      HOME=/tmp/lifeos-build "$provider" --version; \
    done; \
    rm -rf /tmp/lifeos-build
COPY --from=build /out/processor /processor
COPY services/mcp/docker-processor-entrypoint.sh /docker-processor-entrypoint
RUN chmod 0755 /docker-processor-entrypoint
ENV CODEX_HOME=/tmp/lifeos-codex
EXPOSE 8081
ENTRYPOINT ["/docker-processor-entrypoint"]
