# Build from the repository root so the template can use the local shared
# infrastructure module. Generated services pass their own path as SERVICE_DIR.
FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/inbox
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM alpine:3.22
# The capture images directory belongs to the service user, so the
# inbox-media volume mounted there starts out writable.
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos && mkdir -p /var/lib/lifeos/inbox-media && chown -R lifeos:lifeos /var/lib/lifeos
USER lifeos
COPY --from=build /out/service /service

EXPOSE 8080
ENTRYPOINT ["/service"]

