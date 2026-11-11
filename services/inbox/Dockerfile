# Build from the repository root so the template can use the local shared
# infrastructure module. Generated services pass their own path as SERVICE_DIR.
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/inbox
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# The base image is pinned by digest and moves only when Dependabot bumps it;
# Alpine's security fixes land in between, and the release's Trivy gate fails
# on a HIGH or CRITICAL vulnerability that has a fix: the build upgrades the
# packages (SEC-IMG).
# The capture images directory belongs to the service user, so the
# inbox-media volume mounted there starts out writable.
RUN apk upgrade --no-cache \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos && mkdir -p /var/lib/lifeos/inbox-media && chown -R lifeos:lifeos /var/lib/lifeos
USER lifeos
COPY --from=build /out/service /service

EXPOSE 8080
ENTRYPOINT ["/service"]

