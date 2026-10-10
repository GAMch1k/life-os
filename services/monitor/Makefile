.PHONY: build run test docker-build docker-run check

IMAGE ?= lifeos-monitor:local
PORT ?= 8080
MONITOR_STATUS_URL ?= http://localhost:3000/api/status

build:
	mkdir -p bin
	go build -o bin/service ./cmd/server

run:
	SERVICE_NAME=$${SERVICE_NAME:-monitor} PORT=$(PORT) MONITOR_STATUS_URL="$(MONITOR_STATUS_URL)" NOTIFICATIONS_SERVICE_URL="$${NOTIFICATIONS_SERVICE_URL:-}" go run ./cmd/server

test:
	go test ./...

docker-build:
	docker build --build-arg SERVICE_DIR=services/monitor -f Dockerfile -t $(IMAGE) ../..

docker-run:
	docker run --rm --network lifeos-backend -e SERVICE_NAME=monitor -e PORT=8080 -e MONITOR_STATUS_URL=http://dashboard:3000/api/status -e NOTIFICATIONS_SERVICE_URL=http://notifications:8080 $(IMAGE)

check:
	go vet ./...
	go test ./...
	$(MAKE) build
