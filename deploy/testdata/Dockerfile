# syntax=docker/dockerfile:1
FROM golang:1.27.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY deploy/testdata/upstream ./deploy/testdata/upstream
RUN CGO_ENABLED=0 go build -trimpath -o /out/test-upstream ./deploy/testdata/upstream

FROM alpine:3.20
COPY --from=build /out/test-upstream /usr/local/bin/test-upstream
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/test-upstream"]
