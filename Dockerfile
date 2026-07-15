# syntax=docker/dockerfile:1

# --- build ---
# Runs on the native builder platform and cross-compiles to the target, avoiding
# slow emulation under buildx.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

# Cache modules separately from source; the cache mount persists across builds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# Static binary, no cgo, stripped, reproducible; cross-compiled to the target.
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /generalproxy .

# --- run ---
# distroless/static bundles CA certs (needed for HTTPS upstreams); :nonroot runs
# as an unprivileged user. No shell, so debug with `docker run --entrypoint`.
FROM gcr.io/distroless/static:nonroot
COPY --from=build --link /generalproxy /usr/local/bin/generalproxy

WORKDIR /app
EXPOSE 8080

# config.json is not baked in (it's treated as local/secret). Mount it at runtime:
#   docker run -p 8080:8080 -v $PWD/config.json:/app/config.json generalproxy
ENTRYPOINT ["generalproxy"]
CMD ["-config", "/app/config.json", "-port", "8080"]
