# postgres-provision: creates and reconciles every service's database and
# login, then the backup service's read-only login, on the running PostgreSQL
# server (provision-service-databases.sh). compose.yaml runs it after
# PostgreSQL is healthy and before the migrations, at every start, so a fresh
# volume and an update that adds a service's database are both provisioned.
# The scripts and the registry are built in: a server needs no checkout
# (P20-02). Only the PostgreSQL client: it logs in over the network as the
# admin and runs as nobody.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

RUN apk add --no-cache bash postgresql18-client
WORKDIR /opt/lifeos/postgres
COPY --chmod=0755 provision provision-service-databases.sh provision-backup-role.sh verify-database-isolation.sh ./
COPY --chmod=0644 service-databases.conf ./

USER 65534:65534
ENTRYPOINT ["/opt/lifeos/postgres/provision"]
