# syntax=docker/dockerfile:1
FROM golang:1.27.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY lib/edgecore ./lib/edgecore
COPY lib/kernel ./lib/kernel
COPY lib/observability ./lib/observability
COPY proto/gen ./proto/gen
COPY deploy/envoy/testdata ./deploy/envoy/testdata
RUN CGO_ENABLED=0 go build -trimpath -o /out/envoy-fixture ./deploy/envoy/testdata

FROM scratch
COPY --from=build /out/envoy-fixture /usr/local/bin/envoy-fixture
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/envoy-fixture"]
