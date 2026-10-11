.PHONY: build run test generate generate-api migrate-up migrate-down docker-build docker-run check

IMAGE ?= lifeos-service-template:local
PORT ?= 8080

build:
	mkdir -p bin
	go build -o bin/service ./cmd/server

run:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	SERVICE_NAME=$${SERVICE_NAME:-food} PORT=$(PORT) DATABASE_URL="$(DATABASE_URL)" go run ./cmd/server

test:
	go test ./...

generate: generate-api
	sqlc generate -f sqlc.yaml

generate-api:
	oapi-codegen --generate types,fiber-v3 --package generated -o internal/api/generated/openapi.gen.go api/openapi.yaml

migrate-up:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migrate-down:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1

docker-build:
	docker build --build-arg SERVICE_DIR=services/food -f Dockerfile -t $(IMAGE) ../..

docker-run:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	docker run --rm --network lifeos-backend -p 127.0.0.1:$(PORT):8080 -e SERVICE_NAME=$${SERVICE_NAME:-food} -e PORT=8080 -e DATABASE_URL="$(DATABASE_URL)" $(IMAGE)

check: generate
	go vet ./...
	go test ./...
	$(MAKE) build
