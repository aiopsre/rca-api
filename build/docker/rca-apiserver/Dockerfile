# syntax=docker/dockerfile:1.7

# =============================================================================
# 1) Base Images Preparation (Pre-defined base environments)
# =============================================================================
# Define arguments for use in subsequent stages
ARG UID=65532
ARG GID=65532
ARG USER=noroot

# --- A. Debian Base Prep (Includes tool installation and user creation) ---
FROM debian:bookworm AS base-debian-prep
ARG UID
ARG GID
ARG USER
# Install runtime dependencies
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata wget curl telnet \
 && rm -rf /var/lib/apt/lists/*
# Create non-root user
RUN groupadd -g ${GID} ${USER} 2>/dev/null || true \
 && useradd -u ${UID} -g ${USER} ${USER} 2>/dev/null || true

# --- B. Distroless Base Prep (Direct reference) ---
FROM gcr.io/distroless/base-debian12:nonroot AS base-distroless-prep

# =============================================================================
# 2) Selection Logic (Dynamic selection based on DistrolessMode and ENV)
# =============================================================================
# Current configuration mode: auto
# [Mode: auto] Dynamic selection based on build argument ENV
# Default is prod
ARG ENV=prod

# Define Routing Table
FROM base-distroless-prep AS mode-auto-prod
FROM base-debian-prep     AS mode-auto-test
FROM base-debian-prep     AS mode-auto-dev

# Dynamic selection: If ENV=prod point to distroless, otherwise point to debian
FROM mode-auto-${ENV} AS final-base

# =============================================================================
# 3) Builder Stage (Build stage, remains unchanged)
# =============================================================================
ARG BUILDER_IMAGE=golang:1.25.0
FROM ${BUILDER_IMAGE} AS builder

ARG OS
ARG ARCH
ARG UID
ARG GID

WORKDIR /workspace

# Install dependencies for build (tini download)
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Download tini (Static binary)
RUN curl -fsSL -o /usr/bin/tini https://github.com/krallin/tini/releases/download/v0.19.0/tini-static-amd64 \
 && chmod +x /usr/bin/tini

# Go Modules Caching
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
 --mount=type=cache,target=/root/.cache/go-build \
 go mod download

COPY . .

ENV CGO_ENABLED=1 GOOS=${OS} GOARCH=${ARCH} GO111MODULE=on GOCACHE=/root/.cache/go-build GOMODCACHE=/go/pkg/mod

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make build BINS=rca-apiserver

# =============================================================================
# 4) Runtime Stage (Final runtime)
# =============================================================================
# Here FROM uses the final-base calculated in step 2
FROM final-base AS runtime

ARG UID
ARG GID

WORKDIR /app

# Copy artifacts
# Note: Both Tini and the binary are copied from the builder
# No need to run apt-get or useradd, as it was already done in final-base (base-debian-prep)
COPY --from=builder --chown=0:0 /usr/bin/tini /usr/bin/tini
COPY --from=builder --chown=${UID}:${GID} /workspace/_output/platforms/linux/amd64/rca-apiserver /app/rca-apiserver

# Security: run as non-root (UID is 65532 for both distroless and debian)
USER ${UID}:${GID}

ENTRYPOINT ["/usr/bin/tini", "--", "/app/rca-apiserver"]
