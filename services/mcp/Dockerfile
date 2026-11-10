FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
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
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]

# The worker needs the local Codex CLI. Authentication is never part of this
# image: Compose mounts it read-only and the entrypoint copies it to tmpfs.
# tzdata lets TZ (the owner's LIFE_OS_TIMEZONE) take effect for Codex runs.
# npm only installs the Codex CLI, whose launcher needs node alone; npm, npx,
# corepack, and yarn are then removed, as nothing runs them and their bundled
# packages were all of the image's HIGH findings (SEC-AUDIT). codex --version
# fails the build if the CLI needed one of them.
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS processor
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
RUN apk add --no-cache tzdata \
    && npm install --global @openai/codex@0.160.0 \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
        /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
        /opt/yarn-* /usr/local/bin/yarn /usr/local/bin/yarnpkg /root/.npm \
    && codex --version \
    && mkdir -p /workspace \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos
COPY --from=build /out/processor /processor
COPY services/mcp/docker-processor-entrypoint.sh /docker-processor-entrypoint
RUN chmod 0755 /docker-processor-entrypoint
ENV CODEX_HOME=/tmp/lifeos-codex
EXPOSE 8081
ENTRYPOINT ["/docker-processor-entrypoint"]
