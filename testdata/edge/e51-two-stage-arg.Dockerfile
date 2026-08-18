# syntax=docker/dockerfile:1
# Story 9.4's fixture, and it has SEVERAL stages on purpose. A one-stage
# Dockerfile cannot tell "above the first FROM" from "above the FROM being
# changed", and a test written against one asserts a placement rule it cannot
# see. Every instruction below that this story moves a value out of carries a
# comment block, because the declaration goes ABOVE that block — a comment
# documenting a RUN keeps documenting it.

# The build stage pulls the toolchain in.
FROM golang:1.24-alpine AS build
WORKDIR /src
# Cgo is off so the binary is static.
ENV CGO_ENABLED=0
RUN go build -o /app ./cmd/app

# The runtime image is deliberately small.
FROM node:18 AS runtime
COPY --from=build /app /usr/local/bin/app
ENV APP_PORT=8080
ENV APP_GREETING="hello world"
CMD ["app"]

# Pinned by digest. There is no tag here to move, and treating the digest as
# one is exactly the confident wrong answer the refusal exists to prevent.
FROM alpine@sha256:0000000000000000000000000000000000000000000000000000000000000000 AS pinned
RUN true

# No tag at all, which is `:latest` by implication and not a literal anybody
# wrote. Naming a variable after something the file does not say is a guess.
FROM busybox AS untagged
RUN true
