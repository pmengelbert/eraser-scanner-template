# syntax=docker/dockerfile:1.6

# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.21-bookworm AS builder
WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
ENV GOCACHE=/root/gocache
ENV CGO_ENABLED=0
RUN \
    --mount=type=cache,target=${GOCACHE} \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

ARG LDFLAGS
ARG TARGETOS
ARG TARGETARCH

FROM builder AS scanner-build
RUN \
    --mount=type=cache,target=${GOCACHE} \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build ${LDFLAGS:+-ldflags "$LDFLAGS"} -o time-scanner ./example

FROM --platform=$TARGETPLATFORM gcr.io/distroless/static:nonroot AS time-scanner
WORKDIR /
COPY --from=scanner-build /workspace/time-scanner .
ENTRYPOINT ["/time-scanner"]
