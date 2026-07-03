# syntax=docker/dockerfile:1
# Multi-stage build: compiles all five oim binaries in a Go builder image,
# then copies only the statically-linked binaries into a minimal Alpine image.
# The final image is ~30 MB and has no Go toolchain or source code.

FROM golang:1.25-alpine AS builder
WORKDIR /build

# Download dependencies first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/oim-directory    ./cmd/directory  && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/oim-coordinator  ./cmd/coordinator && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/oim              ./cmd/oim         && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/stub-exo         ./cmd/stub-exo    && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/jobgen           ./tools/jobgen

# ── final image ─────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /bin/oim-directory   /usr/local/bin/oim-directory
COPY --from=builder /bin/oim-coordinator /usr/local/bin/oim-coordinator
COPY --from=builder /bin/oim             /usr/local/bin/oim
COPY --from=builder /bin/stub-exo        /usr/local/bin/stub-exo
COPY --from=builder /bin/jobgen          /usr/local/bin/jobgen
