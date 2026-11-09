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

# The worker needs the Codex CLI or, with LIFE_OS_AI_PROVIDER=claude, the
# Claude Code CLI (its musl build needs libgcc and libstdc++). Authentication
# is never part of this image: Compose mounts the Codex home read-only and the
# entrypoint copies it to tmpfs, or passes the Claude subscription token.
# tzdata lets TZ (the owner's LIFE_OS_TIMEZONE) take effect for the runs.
FROM node:22-alpine AS processor
RUN apk add --no-cache tzdata libgcc libstdc++ \
    && npm install --global @openai/codex@0.160.0 @anthropic-ai/claude-code@2.1.319 \
    && mkdir -p /workspace \
    && addgroup -S lifeos && adduser -S -G lifeos lifeos
COPY --from=build /out/processor /processor
COPY services/mcp/docker-processor-entrypoint.sh /docker-processor-entrypoint
RUN chmod 0755 /docker-processor-entrypoint
ENV CODEX_HOME=/tmp/lifeos-codex
EXPOSE 8081
ENTRYPOINT ["/docker-processor-entrypoint"]
