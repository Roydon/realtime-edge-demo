# syntax=docker/dockerfile:1

# Build both binaries once, then ship each from its own minimal final stage.
# Two images, one dependency graph, one place to bump the toolchain.
FROM golang:1.24-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded before the source so that edits to
# application code do not invalidate the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY echo-service/ ./echo-service/
COPY loadgen/ ./loadgen/

ARG VERSION=dev
# CGO is disabled so the result is a static binary that runs on a distroless
# base with no libc. Symbol tables are stripped to keep the image small; the
# version is stamped in so a running container can be traced back to a build.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/echo-service ./echo-service && \
    go build -trimpath -ldflags="-s -w" -o /out/loadgen ./loadgen

# --- runtime: echo-service ---
# Distroless rather than alpine: no shell, no package manager, no busybox, so
# the attack surface of a compromised container is close to nothing. The cost
# is that debugging needs an ephemeral sidecar instead of `docker exec sh`,
# which is the right trade for an internet-facing service.
FROM gcr.io/distroless/static-debian12:nonroot AS echo-service

COPY --from=build /out/echo-service /usr/local/bin/echo-service

# nonroot (uid 65532) is provided by the base image. The service binds 8080,
# an unprivileged port, so it never needs to start as root and drop.
USER nonroot:nonroot
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/echo-service", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/echo-service"]

# --- runtime: loadgen ---
FROM gcr.io/distroless/static-debian12:nonroot AS loadgen

COPY --from=build /out/loadgen /usr/local/bin/loadgen

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/loadgen"]
