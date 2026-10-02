# syntax=docker/dockerfile:1

# ---------- Stage 1: build ----------
# Full Go toolchain; only used to compile. Pinned for reproducible builds.
FROM golang:1.25.14-alpine AS build
WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change, so
# editing code does not re-download every module.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# CGO_ENABLED=0: fully static binaries that need no C library at runtime.
# -trimpath: no local file paths in the binary. -s -w: drop debug symbols.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/createadmin ./cmd/createadmin

# ---------- Stage 2: run ----------
# distroless/static: CA certificates, timezone data and a non-root user, but
# no shell or package manager: very little an attacker could use.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /out/createadmin /usr/local/bin/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
