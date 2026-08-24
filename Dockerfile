# syntax=docker/dockerfile:1

# loon, loon-plugins and loon-baseline are ordinary module dependencies, so
# this image builds from a clone of this repository alone — no sibling
# checkouts, no named build contexts.
FROM golang:1.27 AS build
WORKDIR /app
# GOWORK=off so a developer's go.work (which points at their sibling checkouts,
# and is gitignored) cannot change what ends up inside the image. What ships is
# what go.mod pins.
ENV GOWORK=off
# Modules first: this layer is cached unless the dependencies actually change,
# which is most of the build time on a small source tree.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Stamped by the release workflow from the tag; "dev" for an ordinary local
# build, which is the honest answer — a binary built from a working tree is not
# a release and must not claim to be one.
ARG VERSION=dev
ARG COMMIT=
# ./cmd/loonsite, not . — the root is a library package now (it holds the
# //go:embed of web/, which cannot be declared from a subdirectory).
#
# -X writes the version into the package variables at link time. The paths must
# match the package exactly; a typo does not fail the build, it silently leaves
# the default in place, so version.go's default is "dev" rather than "" — a
# released image reporting "dev" is a visible bug, one reporting nothing is not.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/the-loon-clan/loon-site/web/handlers.Version=${VERSION} \
        -X github.com/the-loon-clan/loon-site/web/handlers.Commit=${COMMIT}" \
      -o /loonsite ./cmd/loonsite

# Static binary (CGO off); templates + static assets are embedded via embed.FS,
# so the runtime image needs nothing but the binary + CA certs (for TLS NNTP).
FROM gcr.io/distroless/static-debian12
COPY --from=build /loonsite /loonsite
EXPOSE 8090
ENTRYPOINT ["/loonsite"]
