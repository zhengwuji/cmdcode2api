FROM golang:1.25-alpine AS build
# Networks that cannot reach proxy.golang.org can override:
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct .
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X cmdcode2api/internal/app.Version=${VERSION}" \
    -o /out/cmdcode2api ./cmd/cmdcode2api

FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S cmdcode2api \
    && adduser -S -G cmdcode2api -h /data cmdcode2api
COPY --from=build /out/cmdcode2api /usr/local/bin/cmdcode2api

# config.yaml and usage.json live in /data — mount a volume there so both
# survive container replacement.
WORKDIR /data
RUN mkdir -p /data && chown cmdcode2api:cmdcode2api /data
VOLUME /data
EXPOSE 11434

# Drop root: the process only needs /data, and running as an unprivileged user
# keeps a file-write bug from reaching the rest of the image.
USER cmdcode2api

# --host 0.0.0.0 makes the gateway reachable from outside the container;
# flags override config.yaml, so no config edit is needed.
ENTRYPOINT ["cmdcode2api"]
CMD ["--host", "0.0.0.0"]

# /health is unauthenticated, so the runtime can probe it without a key.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:11434/health || exit 1
