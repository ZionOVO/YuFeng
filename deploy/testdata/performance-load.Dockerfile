# syntax=docker/dockerfile:1
FROM golang:1.27.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY lib/kernel ./lib/kernel
COPY proto/gen ./proto/gen
COPY scripts/performance-load ./scripts/performance-load
RUN CGO_ENABLED=0 go build -trimpath -o /out/performance-load ./scripts/performance-load

FROM alpine:3.20
COPY --from=build /out/performance-load /usr/local/bin/performance-load
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/performance-load"]
