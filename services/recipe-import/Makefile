.PHONY: build run test generate-api docker-build check

IMAGE ?= lifeos-recipe-import:local
PORT ?= 8080
FOOD_SERVICE_URL ?= http://localhost:8092

build:
	mkdir -p bin
	go build -o bin/service ./cmd/server

run:
	SERVICE_NAME=$${SERVICE_NAME:-recipe-import} PORT=$(PORT) FOOD_SERVICE_URL="$(FOOD_SERVICE_URL)" go run ./cmd/server

test:
	go test ./...

generate-api:
	oapi-codegen --generate types,fiber-v3 --package generated -o internal/api/generated/openapi.gen.go api/openapi.yaml

docker-build:
	docker build --pull --build-arg SERVICE_DIR=services/recipe-import -f Dockerfile -t $(IMAGE) ../..

check: generate-api
	go vet ./...
	go test ./...
	$(MAKE) build
