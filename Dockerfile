# syntax=docker/dockerfile:1

# ── Dashboard build stage ──────────────────────────────────────────────────
# Statically export the Next.js gateway dashboard to admin/out, which the Go
# build below embeds into the binary (see cmd/apicorex/dashboard.go).
FROM node:22-alpine AS dashboard
WORKDIR /admin

COPY cmd/apicorex/admin/package.json cmd/apicorex/admin/package-lock.json* ./
RUN npm install

COPY cmd/apicorex/admin/ ./
RUN npm run build

# ── Build stage ────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache dependencies first (layer reused unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Bring in the exported dashboard UI so //go:embed all:admin/out has content.
COPY --from=dashboard /admin/out ./cmd/apicorex/admin/out

# Build a static, stripped binary.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/apicorex ./cmd/apicorex

# ── Runtime stage ──────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && \
    adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/apicorex /app/apicorex
# config.example.yaml ships as a reference; mount your own at /app/config.yaml.
COPY --from=build /src/config.example.yaml /app/config.example.yaml

USER app
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/apicorex"]
