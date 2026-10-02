# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
VERSION ?= $(shell sed -n 's/^var version = "\(.*\)"/\1/p' cmd/zyntra/main.go)
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)

.PHONY: build build-go web test test-web eval vet fmt check run run-lab run-shop test-e2e deploy docker compose-up helm-lint k8s-manifest deploy-k8s

build: web build-go

build-go:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/zyntra ./cmd/zyntra
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/zyntra-receiver ./examples/receiver

web:
	cd web && npm ci --no-audit --no-fund && npm run build
	mkdir -p web/dist && touch web/dist/.gitkeep

test:
	go test ./...

# test-web runs the console's component tests (Vitest, jsdom; no browser needed).
test-web:
	cd web && npm test

# eval runs the assistant's golden cases (grounding, permissions, action
# selection); run it before changing a model, prompt or retrieval path.
eval:
	go test ./internal/ai -run TestEval -v

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*' -not -path './web/node_modules/*')

check: fmt vet test build

run:
	go run ./cmd/zyntra serve -f examples/kpis.yaml

# Lab model against fake Netra/Gravia/Fabric/Keep sources on :19700.
run-lab: build
	./bin/zyntra fake-sources -addr 127.0.0.1:19700 & \
	ZYNTRA_NETRA_URL=http://127.0.0.1:19700 ZYNTRA_GRAVIA_URL=http://127.0.0.1:19700 \
	ZYNTRA_FABRIC_URL=http://127.0.0.1:19700 ZYNTRA_FABRIC_PASSWORD=fake ZYNTRA_KEEP_URL=http://127.0.0.1:19700 \
	ZYNTRA_API_KEY=$${ZYNTRA_API_KEY:-dev} ./bin/zyntra serve -f packs/gpu; kill %1

# Shop pack from its fixture CSVs, webhooks going to the test receiver on :9099.
run-shop: build
	./bin/zyntra-receiver -addr 127.0.0.1:9099 -dir /tmp/zyntra-inbox & \
	ZYNTRA_POS_URL=http://127.0.0.1:9099/pos ZYNTRA_ERP_URL=http://127.0.0.1:9099/erp \
	ZYNTRA_API_KEY=$${ZYNTRA_API_KEY:-dev} ./bin/zyntra serve -f packs/shop; kill %1

test-e2e: build
	bash scripts/test-e2e.sh

deploy:
	@test -n "$(HOST)" || (echo "usage: make deploy HOST=1.2.3.4 [USER=sus]"; exit 2)
	./scripts/deploy-remote.sh $(HOST) $(or $(USER),sus)

# Container image with packs and the test receiver (docker or podman).
docker:
	$(CONTAINER) build --build-arg VERSION=$(VERSION) -t zyntra:$(VERSION) -t zyntra:latest .

# Shop pack and receiver; needs ZYNTRA_API_KEY in the environment.
compose-up:
	$(CONTAINER) compose up --build

helm-lint:
	helm lint deploy/helm/zyntra
	helm template zyntra deploy/helm/zyntra --set receiver.enabled=true,ingress.enabled=true,networkPolicy.enabled=true >/dev/null

# Plain manifest for clusters without Helm; regenerate after chart changes.
k8s-manifest:
	./scripts/render-k8s.sh > deploy/kubernetes/zyntra.yaml

deploy-k8s:
	@test -n "$(HOST)" || (echo "usage: make deploy-k8s HOST=1.2.3.4 [USER=sus] [PACK=shop]"; exit 2)
	./scripts/deploy-k8s.sh $(HOST) $(or $(USER),sus) --pack $(or $(PACK),shop)
