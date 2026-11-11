# The runtime stage is the PostgreSQL image itself, so pg_dump and pg_restore
# are exactly the server's version. compose.yaml passes its PostgreSQL image
# (LIFE_OS_POSTGRES_IMAGE, else its pinned default); keep this default equal
# to that one, digest included, for a build without compose.
ARG POSTGRES_IMAGE=postgres:18.6-alpine@sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395

# Build from the repository root so the service can use the local shared
# infrastructure module.
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/backup
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM ${POSTGRES_IMAGE}
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# The base image is pinned by digest and moves only when Dependabot bumps it;
# Alpine's security fixes land in between, and the release's Trivy gate fails
# on a HIGH or CRITICAL vulnerability that has a fix: the build upgrades the
# packages. gosu is the PostgreSQL entrypoint's, which this image never runs;
# its old Go standard library was all of the image's remaining findings
# (SEC-IMG).
RUN apk upgrade --no-cache \
    && rm /usr/local/bin/gosu \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos \
    && mkdir -p /etc/lifeos/postgres
USER lifeos
COPY --from=build /out/service /service
# The registry of the databases to back up (BACKUP_DATABASES_FILE), as
# postgres-provision provisions them: built in, so a server needs no
# checkout (P20-02).
COPY --chmod=0644 infra/postgres/service-databases.conf /etc/lifeos/postgres/service-databases.conf

# The PostgreSQL image stops its server with SIGINT and starts it by default;
# this image runs only the service, which stops on SIGTERM.
STOPSIGNAL SIGTERM
EXPOSE 8080
ENTRYPOINT ["/service"]
CMD []
