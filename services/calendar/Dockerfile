# Build from the repository root so the template can use the local shared
# infrastructure module. Generated services pass their own path as SERVICE_DIR.
FROM golang:1.27.3-alpine AS build

WORKDIR /src
COPY . .

ARG SERVICE_DIR=services/calendar
WORKDIR /src/${SERVICE_DIR}
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM alpine:3.22
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service

EXPOSE 8080
ENTRYPOINT ["/service"]

