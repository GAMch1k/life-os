# The runtime stage is the PostgreSQL image itself, so pg_dump and pg_restore
# are exactly the server's version. Keep POSTGRES_IMAGE equal to
# LIFE_OS_POSTGRES_IMAGE; compose passes it.
ARG POSTGRES_IMAGE=postgres:18.6-alpine

# Build from the repository root so the service can use the local shared
# infrastructure module.
FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/backup
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM ${POSTGRES_IMAGE}
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service

# The PostgreSQL image stops its server with SIGINT and starts it by default;
# this image runs only the service, which stops on SIGTERM.
STOPSIGNAL SIGTERM
EXPOSE 8080
ENTRYPOINT ["/service"]
CMD []
