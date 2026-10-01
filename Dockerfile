# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY keep ./keep
COPY web ./web
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zyntra ./cmd/zyntra

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/zyntra /usr/local/bin/zyntra
COPY examples ./examples
EXPOSE 8080
USER nonroot:nonroot
ENV ZYNTRA_STATE_DIR=/tmp/zyntra
ENTRYPOINT ["/usr/local/bin/zyntra"]
CMD ["serve", "-addr", ":8080", "-f", "examples/kpis.yaml"]
