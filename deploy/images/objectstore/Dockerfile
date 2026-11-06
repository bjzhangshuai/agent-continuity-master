# syntax=docker/dockerfile:1.7
# Historical upstream source, for isolated S3 compatibility tests only.
# Images were withdrawn upstream; never substitute an unverified public mirror.
FROM golang:1.27.3-alpine AS build-base
RUN apk add --no-cache ca-certificates git
ENV GOMAXPROCS=2 CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /source

FROM build-base AS minio-build
RUN git init && git remote add origin https://github.com/minio/minio.git && \
    git fetch --depth=1 origin 7aac2a2c5b7c882e68c1ce017d8256be2feea27f && \
    git checkout --detach FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = 7aac2a2c5b7c882e68c1ce017d8256be2feea27f
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -p 2 -trimpath -ldflags='-s -w' -o /out/minio . && \
    git archive --format=tar --output=/out/source.tar HEAD && gzip -n /out/source.tar

FROM build-base AS mc-build
RUN git init && git remote add origin https://github.com/minio/mc.git && \
    git fetch --depth=1 origin 77f82e18b5401a65958f1619df6ebb994634bd88 && \
    git checkout --detach FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = 77f82e18b5401a65958f1619df6ebb994634bd88
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -p 2 -trimpath -ldflags='-s -w' -o /out/mc . && \
    git archive --format=tar --output=/out/source.tar HEAD && gzip -n /out/source.tar

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates curl && \
    addgroup -g 1000 objectstore && adduser -D -u 1000 -G objectstore objectstore && \
    mkdir -p /data /tmp/mc /tmp/minio && chown -R 1000:1000 /data /tmp/mc /tmp/minio
ENV GOMAXPROCS=2 GOMEMLIMIT=192MiB MC_CONFIG_DIR=/tmp/mc
USER 1000:1000
WORKDIR /tmp

FROM runtime AS minio
LABEL org.opencontainers.image.source="https://github.com/minio/minio" \
      org.opencontainers.image.revision="7aac2a2c5b7c882e68c1ce017d8256be2feea27f" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --chmod=0755 deploy/images/objectstore/minio-entrypoint.sh /usr/bin/docker-entrypoint.sh
COPY --from=minio-build /out/minio /usr/bin/minio
COPY --from=minio-build /out/source.tar.gz /usr/share/minio/source.tar.gz
COPY --from=minio-build /source/LICENSE /usr/share/minio/LICENSE
EXPOSE 9000 9001
ENTRYPOINT ["/usr/bin/docker-entrypoint.sh"]
CMD ["server", "--console-address", ":9001", "/data"]

FROM runtime AS mc
LABEL org.opencontainers.image.source="https://github.com/minio/mc" \
      org.opencontainers.image.revision="77f82e18b5401a65958f1619df6ebb994634bd88" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=mc-build /out/mc /usr/bin/mc
COPY --from=mc-build /out/source.tar.gz /usr/share/mc/source.tar.gz
COPY --from=mc-build /source/LICENSE /usr/share/mc/LICENSE
ENTRYPOINT ["/usr/bin/mc"]
