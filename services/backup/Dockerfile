# The runtime stage is the PostgreSQL image itself, so pg_dump and pg_restore
# are exactly the server's version. compose.yaml passes its PostgreSQL image
# (LIFE_OS_POSTGRES_IMAGE, else its pinned default); keep this default equal
# to that one, digest included, for a build without compose.
ARG POSTGRES_IMAGE=postgres:18.6-alpine@sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395

# Build from the repository root so the service can use the local shared
# infrastructure module.
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/backup
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM ${POSTGRES_IMAGE}
# gosu is the PostgreSQL entrypoint's, which this image never runs; its old
# Go standard library was all of the image's remaining findings (SEC-IMG).
RUN rm /usr/local/bin/gosu \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service

# The PostgreSQL image stops its server with SIGINT and starts it by default;
# this image runs only the service, which stops on SIGTERM.
STOPSIGNAL SIGTERM
EXPOSE 8080
ENTRYPOINT ["/service"]
CMD []
