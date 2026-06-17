# ── Build stage ──────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" \
    -o /usr/local/bin/stratus-runtime ./cmd/stratus-runtime/

# ── Runtime stage ───────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /usr/local/bin/stratus-runtime /usr/local/bin/stratus-runtime

EXPOSE 8080 9091

USER nobody:nobody

ENTRYPOINT ["/usr/local/bin/stratus-runtime"]
