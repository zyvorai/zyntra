# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
.PHONY: build test vet fmt check run test-e2e

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/zyntra ./cmd/zyntra

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

check: fmt vet test build

run:
	go run ./cmd/zyntra serve -f examples/kpis.yaml

test-e2e: build
	bash scripts/test-e2e.sh
