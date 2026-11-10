.PHONY: build run test generate-api docker-build check

IMAGE ?= lifeos-host-health:local
PORT ?= 8080

build:
	mkdir -p bin
	go build -o bin/service ./cmd/server

# The made-up host, with no gateway: HOST_HEALTH_FIXTURE=healthy or low-memory.
run:
	SERVICE_NAME=$${SERVICE_NAME:-host-health} PORT=$(PORT) HOST_HEALTH_FIXTURE=$${HOST_HEALTH_FIXTURE:-healthy} go run ./cmd/server

test:
	go test ./...

generate-api:
	oapi-codegen --generate types,fiber-v3 --package generated -o internal/api/generated/openapi.gen.go api/openapi.yaml

docker-build:
	docker build --pull --build-arg SERVICE_DIR=services/host-health -f Dockerfile -t $(IMAGE) ../..

check: generate-api
	go vet ./...
	go test ./...
	$(MAKE) build
