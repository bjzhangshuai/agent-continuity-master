SHELL := /bin/sh
RD_BIN ?= $(HOME)/.rd/bin
export PATH := $(RD_BIN):$(PATH)
GO ?= go
HELM ?= helm
CHART := deploy/helm/agent-continuity

.PHONY: bootstrap generate fmt lint test test-race test-integration test-e2e test-chaos test-performance test-sdk test-device-sdk build docker-build docker-up docker-down migrate-up migrate-down dev helm-lint helm-template deploy-dev deploy-staging smoke-test rollback backup restore-drill

bootstrap:
	$(GO) mod download
	$(GO) version

generate:
	$(GO) generate ./...

fmt:
	gofmt -w $$(rg --files -g '*.go')

lint:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	@test -n "$$CONTINUITY_TEST_DATABASE_URL" || (echo "CONTINUITY_TEST_DATABASE_URL is required" >&2; exit 1)
	$(GO) test -count=1 -v ./tests/integration

test-e2e:
	./scripts/test-e2e-compose.sh

test-chaos:
	$(GO) test -count=1 -v ./tests/chaos ./tests/simulation

test-performance:
	$(GO) test -count=1 -v ./tests/performance

test-sdk:
	cd sdk/python && PYTHONPATH=src python3 -m unittest discover -s tests -v
	cd sdk/typescript && npm ci --ignore-scripts && npm test
	cd sdk/swift && swift run AgentContinuitySelfTest
	cd sdk/kotlin && ./gradlew --no-daemon test
	cd sdk/dotnet && DOTNET_CLI_TELEMETRY_OPTOUT=1 dotnet run --project tests/AgentContinuity.Tests/AgentContinuity.Tests.csproj

test-device-sdk:
	./scripts/test-device-sdk-k8s.sh

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/server ./cmd/server
	$(GO) build -trimpath -o bin/daemon ./cmd/daemon
	$(GO) build -trimpath -o bin/agentcloud ./cmd/agentcloud
	$(GO) build -trimpath -o bin/archive-probe ./cmd/archive-probe

docker-build:
	docker --context rancher-desktop build -t agent-continuity:dev .

docker-up:
	@test -n "$$POSTGRES_PASSWORD" -a -n "$$CONTINUITY_JWT_SECRET" -a -n "$$CONTINUITY_BOOTSTRAP_TOKEN" -a -n "$$CONTINUITY_MASTER_KEY" -a -n "$$MINIO_ROOT_USER" -a -n "$$MINIO_ROOT_PASSWORD" || (echo "database, application, and object-storage secrets are required" >&2; exit 1)
	docker --context rancher-desktop compose -f deploy/compose/docker-compose.yaml up --build -d --wait

docker-down:
	docker --context rancher-desktop compose -f deploy/compose/docker-compose.yaml down

migrate-up:
	$(GO) run ./cmd/server migrate

migrate-down:
	@echo "Down migrations require an explicit backup and manual execution of migrations/001_initial.down.sql" >&2
	@exit 1

dev:
	CONTINUITY_STORE=memory $(GO) run ./cmd/server serve

helm-lint:
	$(HELM) lint $(CHART) -f deploy/environments/dev/values.yaml
	$(HELM) lint $(CHART) -f deploy/environments/staging/values.yaml
	$(HELM) lint $(CHART) -f deploy/environments/production/values.yaml
	$(HELM) lint deploy/helm/agent-continuity-infra
	$(HELM) lint deploy/helm/agent-continuity-infra -f deploy/environments/dev/infra-values.yaml
	$(HELM) lint deploy/helm/agent-continuity-infra -f deploy/environments/production/infra-observability-values.yaml
	python3 scripts/test-helm-contracts.py

helm-template:
	$(HELM) template agent-continuity $(CHART) -f deploy/environments/dev/values.yaml >/dev/null

deploy-dev:
	./scripts/deploy-rancher-desktop.sh

deploy-staging:
	@echo "Set an explicit staging kube-context and secret workflow; local safety script intentionally deploys only dev." >&2
	@exit 1

smoke-test:
	./scripts/smoke-k8s.sh

rollback:
	./scripts/rollback-rancher-desktop.sh

backup:
	./scripts/backup.sh

restore-drill:
	./scripts/restore-drill.sh
