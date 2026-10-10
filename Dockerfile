# syntax=docker/dockerfile:1

# ── Dashboard build stage ──────────────────────────────────────────────────
# Statically export the Next.js gateway dashboard to admin/out, which the Go
# build below embeds into the binary (see cmd/apicorex/dashboard.go).
FROM node:22-alpine AS dashboard
WORKDIR /admin

COPY cmd/apicorex/admin/package.json cmd/apicorex/admin/package-lock.json ./
RUN npm ci

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
#
# The compile cache is mounted rather than left in the layer: COPY . . above
# changes on every commit, so without it every build compiles every package
# from nothing — about six minutes of CPU on a fast machine, most of it
# dependencies that did not change (the SQLite driver alone is two). With it,
# only what changed is compiled. The first build on a fresh agent still pays
# the full cost; the Jenkinsfile's timeout leaves room for that.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/apicorex ./cmd/apicorex

# ── Runtime stage ──────────────────────────────────────────────────────────
FROM alpine:3.20
# rclone copies the store's snapshots off the server when STORE_BACKUP_REMOTE
# is set (internal/snapshots).
RUN apk add --no-cache ca-certificates wget rclone && \
    adduser -D -u 10001 app && \
    mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=build /out/apicorex /app/apicorex
# config.example.yaml ships as a reference; mount your own at /app/config.yaml.
COPY --from=build /src/config.example.yaml /app/config.example.yaml

# The config store (internal/store). Owned by app so the non-root process can
# create it; a named volume mounted here starts with this ownership.
VOLUME /data
ENV STORE_PATH=/data/core.db

USER app
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- "http://localhost${HTTP_PORT:-:8080}/health" || exit 1

ENTRYPOINT ["/app/apicorex"]
