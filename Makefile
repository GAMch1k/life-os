.PHONY: infra-env infra-up infra-down infra-ps infra-logs infra-reset core-up core-smoke life-up life-smoke verify-clean-db verify-isolation e2e-smoke processor-smoke processor-codex-actions-smoke human-review-smoke activity-trace-smoke runtime-status-smoke retry-schedule-smoke codex-runtime-smoke relative-dates-smoke cross-language-smoke calendar-ai-smoke phase8-acceptance browser-e2e action-idempotency-smoke voice-lease-recovery-smoke db-provision db-provision-backup db-check-isolation db-migration-new db-generate db-migrate-up db-migrate-down db-test create-service create-service-smoke dashboard-check check

# Targets that could recreate or delete the deployed Life OS (the Compose
# project lifeos, which compose.yaml names, from any checkout) refuse unless
# CONFIRM names the target: make infra-reset CONFIRM=infra-reset.
LIVE_PROJECT := lifeos

# $(call refuse_if_deployed,<what it would do>): refuse when the lifeos
# project has containers, a running deployment.
define refuse_if_deployed
	@if [ "$(CONFIRM)" != "$@" ] && [ -n "$$(docker ps -aq --filter label=com.docker.compose.project=$(LIVE_PROJECT))" ]; then \
	  printf '%s\n' "make $@: Life OS is deployed here (Compose project $(LIVE_PROJECT)). $(1)" \
	    "Redeploy one service at a time instead: docker compose build --pull <service>, then docker compose up -d --no-build --no-deps <service>." \
	    "To run it anyway: make $@ CONFIRM=$@" >&2; \
	  exit 2; \
	fi
endef

# $(call confirm,<what it does>): refuse unless CONFIRM names the target.
define confirm
	@if [ "$(CONFIRM)" != "$@" ]; then \
	  printf '%s\n' "make $@: $(1)" "To run it anyway: make $@ CONFIRM=$@" >&2; \
	  exit 2; \
	fi
endef

infra-env:
	@test -f .env || cp .env.example .env

# A first start. On a deployment they would also recreate every service
# whose settings changed, PostgreSQL included.
infra-up: infra-env
	$(call refuse_if_deployed,It would recreate every service whose settings changed and PostgreSQL with them.)
	docker compose up --detach --wait

# --pull: every build starts from the current base images, not from older
# ones Docker has kept (docs/operations/ci.md#dependabot). Every module's
# image is built, as the verification stack runs them all; up starts the
# core and the modules COMPOSE_PROFILES in .env turns on.
core-up life-up: infra-env
	$(call refuse_if_deployed,It would rebuild every image and recreate every changed service and PostgreSQL with them.)
	COMPOSE_PROFILES=$$(bash -c '. tools/lib/modules.bash && IFS=, && printf "%s" "$${module_ids[*]}"') docker compose build --pull
	docker compose up --detach

# The core-service and Dashboard smokes on the disposable verification stack
# (tools/smoke-life), from the images already built; never on live data.
core-smoke: infra-env
	./tools/smoke-life --core

life-smoke: infra-env
	./tools/smoke-life

verify-clean-db: infra-env
	./tools/verify-compose up --detach --no-build
	./tools/verify-compose run --rm --no-deps -T postgres-provision verify-database-isolation.sh
	./tools/verify-compose down --volumes --remove-orphans

verify-isolation: infra-env
	./tools/verify-isolation

e2e-smoke: infra-env
	./tools/smoke-e2e

processor-smoke: infra-env
	./tools/smoke-processor-runtime

processor-codex-actions-smoke: infra-env
	./tools/smoke-processor-codex-actions

human-review-smoke: infra-env
	docker compose build --pull inbox tasks mcp processor
	./tools/smoke-human-review

activity-trace-smoke: infra-env
	docker compose build --pull activity voice voice-worker inbox tasks mcp processor
	./tools/smoke-activity-trace

# Images are built one at a time to keep peak memory low on small hosts.
runtime-status-smoke: infra-env
	for service in dashboard processor mcp inbox voice voice-worker; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-runtime-status

retry-schedule-smoke: infra-env
	for service in inbox mcp processor dashboard; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-retry-schedule

codex-runtime-smoke: infra-env
	for service in inbox mcp processor dashboard; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-codex-runtime

# Real Codex with LIFE_OS_TIMEZONE=Europe/Berlin: relative dates in captures.
relative-dates-smoke: infra-env
	for service in mcp processor; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-relative-dates

# Real Codex and whisper.cpp: "I already bought the chicken" completes a Task
# titled in Russian or German; two fitting Tasks still go to Human Review.
cross-language-smoke: infra-env
	for service in mcp processor; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-cross-language

# Real Codex with LIFE_OS_TIMEZONE=Europe/Berlin: Calendar create with a
# location, reschedules (also across languages), and ambiguity and
# cancellation through Human Review, then an approved cancellation.
calendar-ai-smoke: infra-env
	for service in mcp processor; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-calendar-ai

phase8-acceptance: infra-env
	for service in activity voice voice-worker inbox tasks mcp processor dashboard; do docker compose build --pull $$service || exit 1; done
	./tools/smoke-phase8-acceptance

browser-e2e: infra-env
	./tools/browser-e2e

action-idempotency-smoke: infra-env
	./tools/smoke-action-idempotency

voice-lease-recovery-smoke: infra-env
	docker compose build --pull voice voice-worker
	./tools/smoke-voice-lease-recovery

infra-down:
	docker compose down --remove-orphans

infra-ps:
	docker compose ps

infra-logs:
	docker compose logs --follow postgres

infra-reset:
	$(call confirm,It deletes the volumes of Compose project $(LIVE_PROJECT): every database and the capture images and Voice audio. Back up first.)
	docker compose down --volumes --remove-orphans

# On the running server, from the postgres-provision image (infra/postgres;
# after a change there, docker compose build postgres-provision first) with
# the passwords in .env, as every start runs it: every service's database and
# login, then the backup role. Idempotent; --no-deps leaves PostgreSQL as it is.
db-provision: infra-env
	docker compose run --rm --no-deps -T postgres-provision

# The backup service's read-only login alone (idempotent; see
# docs/services/backup.md). No restart needed.
db-provision-backup: infra-env
	docker compose run --rm --no-deps -T postgres-provision provision-backup-role.sh

db-check-isolation: infra-env
	docker compose run --rm --no-deps -T postgres-provision verify-database-isolation.sh

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
	$(call confirm,It migrates the $(SERVICE) database at DATABASE_URL down one step: a down migration can drop tables and their data.)
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
