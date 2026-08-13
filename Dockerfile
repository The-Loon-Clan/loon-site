# syntax=docker/dockerfile:1

# loon, loon-plugins and loon-baseline are ordinary module dependencies, so
# this image builds from a clone of this repository alone — no sibling
# checkouts, no named build contexts.
FROM golang:1.26 AS build
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
# ./cmd/loonsite, not . — the root is a library package now (it holds the
# //go:embed of web/, which cannot be declared from a subdirectory).
RUN CGO_ENABLED=0 go build -trimpath -o /loonsite ./cmd/loonsite

# Static binary (CGO off); templates + static assets are embedded via embed.FS,
# so the runtime image needs nothing but the binary + CA certs (for TLS NNTP).
FROM gcr.io/distroless/static-debian12
COPY --from=build /loonsite /loonsite
EXPOSE 8090
ENTRYPOINT ["/loonsite"]
