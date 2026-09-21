# syntax=docker/dockerfile:1

# The default build is a fully static binary on distroless/static.
#
# For a position-independent binary pass both build args below. Go's internal
# linker still emits a dynamic loader reference for PIE, so the runtime image
# must carry one:
#   --build-arg BUILDMODE=pie --build-arg RUNTIME_BASE=gcr.io/distroless/base-debian12:nonroot
ARG RUNTIME_BASE=gcr.io/distroless/static-debian12:nonroot

# Build the page. It is written into internal/site/dist for the Go embed.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY internal/site/dist/.keep /src/internal/site/dist/.keep
RUN npm run build

# Build one static binary with the page embedded.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILDMODE=exe
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY internal/ ./internal/
COPY --from=web /src/internal/site/dist/ ./internal/site/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -buildmode=$BUILDMODE -trimpath -ldflags="-s -w" -o /out/global-mesh .

FROM ${RUNTIME_BASE}
COPY --from=build /out/global-mesh /global-mesh
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/global-mesh"]
