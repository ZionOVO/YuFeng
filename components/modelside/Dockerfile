FROM python:3.12-slim AS build

WORKDIR /src
COPY components/modelside/pyproject.toml ./
COPY components/modelside/yufeng_modelside ./yufeng_modelside
RUN python -m pip wheel --no-cache-dir --wheel-dir /wheels '.[tensorflow]'

FROM python:3.12-slim

ARG VERSION=dev
ARG SHA=unknown
ARG BUILT_AT=unknown
LABEL org.opencontainers.image.title="yufeng-modelside" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${SHA}" \
      org.opencontainers.image.created="${BUILT_AT}"

RUN groupadd --system --gid 65532 yufeng \
    && useradd --system --uid 65532 --gid yufeng --home /nonexistent yufeng \
    && mkdir -p /run/yufeng /var/lib/yufeng/modelside \
    && chown -R 65532:65532 /run/yufeng /var/lib/yufeng
COPY --from=build /wheels /wheels
RUN python -m pip install --no-cache-dir /wheels/* && rm -rf /wheels
USER 65532:65532
ENTRYPOINT ["yufeng-modelside"]
