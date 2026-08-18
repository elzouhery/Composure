FROM golang:1.22-alpine AS build
WORKDIR /src
RUN go build ./cmd/...

FROM alpine:3.19
COPY --from=build /src/app /usr/local/bin/app
CMD ["app"]
