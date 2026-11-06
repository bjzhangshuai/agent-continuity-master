# syntax=docker/dockerfile:1.7
FROM golang:1.26.7-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOMAXPROCS=2 CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server && \
    GOMAXPROCS=2 CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/daemon ./cmd/daemon && \
    GOMAXPROCS=2 CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/agentcloud ./cmd/agentcloud && \
    GOMAXPROCS=2 CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/archive-probe ./cmd/archive-probe

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 continuity && adduser -D -H -u 10001 -G continuity continuity && \
    mkdir -p /data/artifacts && chown -R 10001:10001 /data
WORKDIR /app
COPY --from=build --chown=10001:10001 /out/server /out/daemon /out/agentcloud /out/archive-probe /app/
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["serve"]
