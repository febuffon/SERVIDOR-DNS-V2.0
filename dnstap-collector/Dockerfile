# ============================================================
# Dockerfile - dnstap-collector
# Multi-stage: compila em Go alpine, roda em scratch
# ============================================================

# Stage 1: Build
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copia dependencias primeiro (cache de layer)
COPY go.mod go.sum ./
RUN go mod download

# Compila binario estatico
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o dnstap-collector .

# ============================================================
# Stage 2: Runtime minimo (alpine para suportar healthcheck via shell)
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

# Binario
COPY --from=builder /build/dnstap-collector /dnstap-collector

ENTRYPOINT ["/dnstap-collector"]
