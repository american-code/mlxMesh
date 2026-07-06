# syntax=docker/dockerfile:1
# Multi-stage build: compiles all five oim binaries in a Go builder image,
# then copies only the statically-linked binaries into a minimal Alpine image.
# The final image is ~30 MB and has no Go toolchain or source code.
#
# Version stamping: pass --build-arg VERSION=... COMMIT=... BUILD_DATE=... so the
# binaries report their build via `oim version` and the coordinator/directory
# startup logs. Defaults keep a plain `docker build` working. For reproducible
# images, pin BUILDKIT to a digest and pass a fixed BUILD_DATE (SOURCE_DATE_EPOCH).

FROM golang:1.25-alpine AS builder
WORKDIR /build

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# Download dependencies first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# -buildid= + -trimpath make each binary reproducible; the version package is
# stamped via -X so support/incident triage can identify the exact build.
ENV VPKG=github.com/open-inference-mesh/oim/internal/version
RUN LDFLAGS="-s -w -buildid= -X ${VPKG}.Version=${VERSION} -X ${VPKG}.Commit=${COMMIT} -X ${VPKG}.Date=${BUILD_DATE}" && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$LDFLAGS" -o /bin/oim-directory    ./cmd/directory  && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$LDFLAGS" -o /bin/oim-coordinator  ./cmd/coordinator && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$LDFLAGS" -o /bin/oim              ./cmd/oim         && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$LDFLAGS" -o /bin/stub-exo         ./cmd/stub-exo    && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$LDFLAGS" -o /bin/jobgen           ./tools/jobgen

# ── final image ─────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates wget

ARG VERSION=dev
ARG COMMIT=none
# OCI labels so a running image's provenance is inspectable via `docker inspect`.
LABEL org.opencontainers.image.title="mlxMesh (Open Inference Mesh)" \
      org.opencontainers.image.source="https://github.com/open-inference-mesh/oim" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="AGPL-3.0"

COPY --from=builder /bin/oim-directory   /usr/local/bin/oim-directory
COPY --from=builder /bin/oim-coordinator /usr/local/bin/oim-coordinator
COPY --from=builder /bin/oim             /usr/local/bin/oim
COPY --from=builder /bin/stub-exo        /usr/local/bin/stub-exo
COPY --from=builder /bin/jobgen          /usr/local/bin/jobgen
