FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/services/mcp
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server
RUN go build -trimpath -ldflags="-s -w" -o /out/processor ./cmd/processor

FROM alpine:3.22 AS service
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]

# The worker needs the local Codex CLI. Authentication is never part of this
# image: Compose mounts it read-only and the entrypoint copies it to tmpfs.
FROM node:22-alpine AS processor
RUN npm install --global @openai/codex@0.160.0 \
    && mkdir -p /workspace \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos
COPY --from=build /out/processor /processor
COPY services/mcp/docker-processor-entrypoint.sh /docker-processor-entrypoint
RUN chmod 0755 /docker-processor-entrypoint
ENV CODEX_HOME=/var/lib/lifeos/codex
EXPOSE 8081
ENTRYPOINT ["/docker-processor-entrypoint"]
