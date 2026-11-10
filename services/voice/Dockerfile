# Build from the repository root so the template can use the local shared
# infrastructure module. Generated services pass their own path as SERVICE_DIR.
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/voice
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server
RUN go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos && mkdir -p /var/lib/lifeos/voice && chown -R lifeos:lifeos /var/lib/lifeos
USER lifeos
COPY --from=build /out/service /service
COPY --from=build /out/worker /worker

EXPOSE 8080
ENTRYPOINT ["/service"]
