# syntax=docker/dockerfile:1

# ── Build stage ────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache dependencies first (layer reused unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# Build a static, stripped binary.
COPY . .
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
