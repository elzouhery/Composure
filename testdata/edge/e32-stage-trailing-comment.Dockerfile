# a two-stage build whose first stage ends with a comment
FROM golang:1.24 AS builder
WORKDIR /src
RUN go build ./...

# the runtime image
FROM alpine:3.20
COPY --from=builder /src/app /app
