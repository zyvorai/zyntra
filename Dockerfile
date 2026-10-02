# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0
#
# The image carries packs and HTTP/file/SQL/REST sources. kubectl is not in
# the image, so kubectl *actions* and the node-inventory KPI adapter need the
# binary install (scripts/deploy-remote.sh) or a sidecar that provides it.
# Ontology Kubernetes connectors do not need it: in a pod they read the cluster
# through the service account (helm: kubernetes.inCluster=true grants a
# read-only ClusterRole, never secrets).
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG VERSION=0.4.0-dev
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY keep ./keep
COPY examples ./examples
COPY web ./web
COPY --from=web /web/dist ./web/dist
RUN export CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
 && go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/zyntra ./cmd/zyntra \
 && go build -trimpath -ldflags="-s -w" -o /out/zyntra-receiver ./examples/receiver \
 && mkdir -p /out/state/out /out/inbox

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=0.4.0-dev
LABEL org.opencontainers.image.title="zyntra" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.vendor="Zyvor AI Labs" \
      org.opencontainers.image.source="https://github.com/zyvorai/zyntra" \
      org.opencontainers.image.licenses="LicenseRef-Zyvor-Production-1.0"
WORKDIR /app
COPY --from=build /out/zyntra /usr/local/bin/zyntra
COPY --from=build /out/zyntra-receiver /usr/local/bin/zyntra-receiver
COPY --from=build --chown=65532:65532 /out/state /var/lib/zyntra
COPY --from=build --chown=65532:65532 /out/inbox /var/lib/zyntra-inbox
COPY examples ./examples
COPY packs ./packs
USER 65532:65532
ENV ZYNTRA_STATE_DIR=/var/lib/zyntra \
    ZYNTRA_OUTPUT_DIR=/var/lib/zyntra/out \
    ZYNTRA_LISTEN=:8080
VOLUME ["/var/lib/zyntra"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/zyntra"]
CMD ["serve", "-f", "packs/shop"]
