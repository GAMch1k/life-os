# Build from the repository root so the service can use the local shared
# infrastructure module.
FROM golang:1.27.3-alpine AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/host-health
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM alpine:3.22
COPY --from=build /out/service /service

# nobody: host-health may read only what any unprivileged user could; the
# docker gateway (`/service docker-gateway`) adds the Docker socket's group
# in compose.
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/service"]
