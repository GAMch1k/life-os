# The migrations of every service, at /migrations/<service>, and
# golang-migrate to apply them: compose.yaml's <service>-migrate runs this
# image with -path /migrations/<service> (P20-02: no checkout needed). The
# migrate binary is built from source with only the PostgreSQL driver and the
# file source; the upstream image carries every driver and their
# vulnerabilities. go install checks the module against the Go checksum
# database. Update MIGRATE_VERSION deliberately, as a ticket.
# Built from the repository root (the services' migrations).
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ARG MIGRATE_VERSION=v4.20.1
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags="-s -w -X main.Version=${MIGRATE_VERSION}" -tags "postgres file" \
      "github.com/golang-migrate/migrate/v4/cmd/migrate@${MIGRATE_VERSION}"
# Every service with migrations, each into a directory of its own name; a new
# service's are picked up without a change here.
COPY services /src/services
RUN mkdir /out/migrations \
    && for migrations in /src/services/*/db/migrations; do \
         service=${migrations#/src/services/}; service=${service%%/*}; \
         cp -R "$migrations" "/out/migrations/$service"; \
       done \
    && chmod -R a+rX,go-w /out/migrations

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/migrations /migrations
USER 65534:65534
ENTRYPOINT ["migrate"]
