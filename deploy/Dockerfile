# syntax=docker/dockerfile:1
FROM node:22-alpine AS console
WORKDIR /console
COPY console/package.json console/package-lock.json ./
RUN npm ci
COPY console/ ./
RUN npm run build

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
      -o /out/yufeng-brain ./cmd/yufeng-brain && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yufeng-jarvis ./cmd/yufeng-jarvis && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yufeng-agentd ./cmd/yufeng-agentd && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yufeng-host ./cmd/yufeng-host && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yufeng-run ./cmd/yufeng-run && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.sha=${SHA} -X main.builtAt=${BUILT_AT}" \
      -o /out/yfctl ./cmd/yfctl

FROM alpine:3.20
RUN addgroup -S yufeng && adduser -S yufeng -G yufeng \
    && apk add --no-cache wget \
    && mkdir -p /var/lib/yufeng/cache /var/lib/yufeng/telemetry /var/lib/yufeng/jarvis /usr/share/yufeng/console \
    && chown -R yufeng:yufeng /var/lib/yufeng /usr/share/yufeng
COPY --from=build /out/yufeng-brain /out/yufeng-jarvis /out/yufeng-agentd /out/yufeng-host /out/yufeng-run /out/yfctl /usr/local/bin/
COPY --from=console /console/dist /usr/share/yufeng/console
USER yufeng
ENTRYPOINT ["/usr/local/bin/yufeng-brain"]
