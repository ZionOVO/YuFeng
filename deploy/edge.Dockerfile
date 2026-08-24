# syntax=docker/dockerfile:1
FROM golang:1.27.0-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG SHA=unknown
ARG BUILT_AT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yufeng-edge ./cmd/yufeng-edge

FROM alpine:3.20
ARG VERSION=dev
ARG SHA=unknown
ARG BUILT_AT=unknown
LABEL org.opencontainers.image.title="yufeng-edge" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${SHA}" \
      org.opencontainers.image.created="${BUILT_AT}"
RUN addgroup -S -g 65532 yufeng \
    && adduser -S -D -H -u 65532 -G yufeng yufeng \
    && apk add --no-cache wget \
    && mkdir -p /run/yufeng /var/lib/yufeng/edge /var/lib/yufeng/telemetry \
    && chown -R 65532:65532 /run/yufeng /var/lib/yufeng
COPY --from=build /out/yufeng-edge /usr/local/bin/yufeng-edge
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/yufeng-edge"]
