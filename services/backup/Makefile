.PHONY: build run test generate-api docker-build check

IMAGE ?= lifeos-backup:local
PORT ?= 8080

build:
	mkdir -p bin
	go build -o bin/service ./cmd/server

# Runs against a local PostgreSQL with pg_dump and pg_restore on PATH, writing
# to ./bin/backups. BACKUP_DATABASE_URL names the server and login, not a
# database.
run:
	@test -n "$(BACKUP_DATABASE_URL)" || (echo "BACKUP_DATABASE_URL is required" >&2; exit 2)
	mkdir -p bin/backups
	SERVICE_NAME=$${SERVICE_NAME:-backup} PORT=$(PORT) BACKUP_DATABASE_URL="$(BACKUP_DATABASE_URL)" BACKUP_DATABASES_FILE=$(abspath ../../infra/postgres/service-databases.conf) BACKUP_DIR=$(CURDIR)/bin/backups go run ./cmd/server

test:
	go test ./...

generate-api:
	oapi-codegen --generate types,fiber-v3 --package generated -o internal/api/generated/openapi.gen.go api/openapi.yaml

docker-build:
	docker build --build-arg SERVICE_DIR=services/backup -f Dockerfile -t $(IMAGE) ../..

check: generate-api
	go vet ./...
	go test ./...
	$(MAKE) build
