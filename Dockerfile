# syntax=docker/dockerfile:1
#
# Local / self-hosted image build:
#   docker build --build-arg VERSION=$(git describe --tags --always) -t oskar:dev .
#   docker buildx build --platform linux/amd64,linux/arm64 ...
#
# Official releases are built by GoReleaser from Dockerfile.goreleaser, which
# reuses the release binaries on the same distroless base.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X github.com/alessandrocorsico/oskar/internal/cli.version=${VERSION}" \
      -o /oskar ./cmd/oskar

# Runtime stage: distroless static, non-root (uid 65532), no shell.
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="oskar" \
      org.opencontainers.image.description="Open Source Kubernetes Anomaly Radar: read-only scan for silent state inconsistencies" \
      org.opencontainers.image.source="https://github.com/alessandrocorsico/oskar" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /oskar /oskar
USER 65532:65532
ENTRYPOINT ["/oskar"]
CMD ["scan"]
