# A Dockerfile written on Windows.
FROM alpine:3.20 AS build
RUN apk add --no-cache git

FROM alpine:3.20
COPY --from=build /out /out
CMD ["/out/app"]
