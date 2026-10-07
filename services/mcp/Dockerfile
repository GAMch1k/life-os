FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/services/mcp
RUN go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/server

FROM alpine:3.22
RUN addgroup -S lifeos && adduser -S -G lifeos lifeos
USER lifeos
COPY --from=build /out/service /service
EXPOSE 8080
ENTRYPOINT ["/service"]
