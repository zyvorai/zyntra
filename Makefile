# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
.PHONY: build build-go web test vet fmt check run run-lab test-e2e deploy

build: web build-go

build-go:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/zyntra ./cmd/zyntra

web:
	cd web && npm ci --no-audit --no-fund && npm run build
	mkdir -p web/dist && touch web/dist/.gitkeep

test:
	go test ./...

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
	ZYNTRA_API_KEY=$${ZYNTRA_API_KEY:-dev} ./bin/zyntra serve -f examples/lab-kpis.yaml; kill %1

test-e2e: build
	bash scripts/test-e2e.sh

deploy:
	@test -n "$(HOST)" || (echo "usage: make deploy HOST=1.2.3.4 [USER=sus]"; exit 2)
	./scripts/deploy-remote.sh $(HOST) $(or $(USER),sus)
