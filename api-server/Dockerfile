# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/dist/server ./cmd/server

# ── Runtime stage ───────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

# Link the published GHCR package back to this repo (shows "in Startup2Team/
# Rides-api" on the package page, and lets GitHub apply the repo's access).
LABEL org.opencontainers.image.source="https://github.com/Startup2Team/Rides-api" \
      org.opencontainers.image.description="Rides API — Go backend for the Rides platform"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/dist/server /app/server
COPY --from=builder /app/migrations  /app/migrations
COPY --from=builder /app/config      /app/config

# Non-root user for security
RUN adduser --disabled-password --gecos "" appuser
USER appuser

EXPOSE 8080

ENTRYPOINT ["/app/server"]
