.PHONY: infra-env infra-up infra-down infra-ps infra-logs infra-reset core-up core-smoke life-up life-smoke verify-clean-db db-provision db-check-isolation db-migration-new db-generate db-migrate-up db-migrate-down db-test create-service create-service-smoke dashboard-check check

infra-env:
	@test -f .env || cp .env.example .env

infra-up: infra-env
	docker compose up --detach --wait

core-up: infra-env
	docker compose up --detach --build

core-smoke: core-up
	./tools/smoke-core-services

life-up: core-up

life-smoke: core-smoke
	./tools/smoke-dashboard

verify-clean-db: infra-env
	docker compose -f compose.yaml -f compose.verify.yaml up --detach --no-build
	docker compose -f compose.yaml -f compose.verify.yaml exec -T postgres /opt/lifeos/postgres/verify-database-isolation.sh
	docker compose -f compose.yaml -f compose.verify.yaml down --volumes --remove-orphans

infra-down:
	docker compose down --remove-orphans

infra-ps:
	docker compose ps

infra-logs:
	docker compose logs --follow postgres

infra-reset:
	docker compose down --volumes --remove-orphans

db-provision: infra-env
	docker compose exec -T postgres /docker-entrypoint-initdb.d/10-provision-service-databases.sh

db-check-isolation: infra-env
	docker compose exec -T postgres /opt/lifeos/postgres/verify-database-isolation.sh

db-migration-new:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	@test -n "$(NAME)" || (echo "NAME is required" >&2; exit 2)
	@test -d "services/$(SERVICE)/db/migrations" || (echo "unknown service or missing migrations directory: $(SERVICE)" >&2; exit 2)
	migrate create -seq -digits 6 -ext sql -dir "services/$(SERVICE)/db/migrations" "$(NAME)"

db-generate:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	@test -f "services/$(SERVICE)/sqlc.yaml" || (echo "unknown service or missing sqlc.yaml: $(SERVICE)" >&2; exit 2)
	cd "services/$(SERVICE)" && sqlc generate -f sqlc.yaml

db-migrate-up:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	@test -d "services/$(SERVICE)/db/migrations" || (echo "unknown service or missing migrations directory: $(SERVICE)" >&2; exit 2)
	migrate -path "services/$(SERVICE)/db/migrations" -database "$(DATABASE_URL)" up

db-migrate-down:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 2)
	@test -d "services/$(SERVICE)/db/migrations" || (echo "unknown service or missing migrations directory: $(SERVICE)" >&2; exit 2)
	migrate -path "services/$(SERVICE)/db/migrations" -database "$(DATABASE_URL)" down 1

db-test:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	@test -f "services/$(SERVICE)/go.mod" || (echo "unknown service or missing go.mod: $(SERVICE)" >&2; exit 2)
	cd "services/$(SERVICE)" && go test ./...

create-service:
	@test -n "$(SERVICE)" || (echo "SERVICE is required" >&2; exit 2)
	./tools/create-service "$(SERVICE)"

create-service-smoke:
	./tools/test-create-service

dashboard-check:
	npm --prefix apps/dashboard run check

check:
	./tools/check
