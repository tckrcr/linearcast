# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.1

# Runtime base pinned to an alpine:edge snapshot that ships ffmpeg 8.1.2, which
# carries the 2026-01-26 readrate_initial_burst fix (issue #21510). Stable Alpine
# (3.22/3.23) still ships the broken 8.0.1 where the burst is a silent no-op, so
# on-demand HEVC long-GOP cold-start cannot fast-fill. edge is a rolling target,
# so the DIGEST is the version pin: an unpinned `apk add ffmpeg` would drift off
# the verified build. To bump, see deploy/ffmpeg-pin.md (records the verification).
ARG ALPINE_RUNTIME=alpine:edge@sha256:9a341ff2287c54b86425cbee0141114d811ae69d88a36019087be6d896cef241

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast ./cmd/linearcast && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast-admin ./cmd/linearcast-admin && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast-extender ./cmd/linearcast-extender && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast-ingest ./cmd/linearcast-ingest && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast-encoder ./cmd/linearcast-encoder && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/linearcast-maint ./cmd/linearcast-maint

# Cross-compile linearcast-encoder for all supported client platforms so the
# admin server can hand them out from the UI. These are the binaries operators
# download after registering an API key.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    mkdir -p /out/encoder-dist && \
    CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/encoder-dist/linearcast-encoder-darwin-arm64  ./cmd/linearcast-encoder && \
    CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/encoder-dist/linearcast-encoder-darwin-amd64  ./cmd/linearcast-encoder && \
    CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/encoder-dist/linearcast-encoder-linux-amd64   ./cmd/linearcast-encoder && \
    CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/encoder-dist/linearcast-encoder-linux-arm64   ./cmd/linearcast-encoder && \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/encoder-dist/linearcast-encoder-windows-amd64.exe ./cmd/linearcast-encoder

FROM node:20-alpine AS web-build

WORKDIR /src/web-ui

COPY web-ui/package.json web-ui/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi

COPY web-ui/tsconfig.json web-ui/vite.config.ts web-ui/index.html ./
COPY web-ui/src ./src

RUN npm run build

FROM ${ALPINE_RUNTIME} AS runtime

# ffmpeg version is fixed by the ALPINE_RUNTIME digest above (8.1.2), so no apk
# version pin here — edge drops old -rN revisions, which would break a pinned
# build. The digest snapshot is what guarantees a known-good, burst-capable build.
RUN apk add --no-cache bash ca-certificates ffmpeg gettext-envsubst nginx tzdata && \
    mkdir -p /app /data/linearcast /data/cache /data/media \
      /usr/share/nginx/html /tmp/nginx/client-body /tmp/nginx/proxy \
      /tmp/nginx/fastcgi /tmp/nginx/uwsgi /tmp/nginx/scgi && \
    chmod -R 1777 /tmp/nginx && \
    rm -f /etc/nginx/http.d/default.conf /etc/nginx/conf.d/default.conf

COPY --from=build /out/linearcast /usr/local/bin/linearcast
COPY --from=build /out/linearcast-admin /usr/local/bin/linearcast-admin
COPY --from=build /out/linearcast-extender /usr/local/bin/linearcast-extender
COPY --from=build /out/linearcast-ingest /usr/local/bin/linearcast-ingest
COPY --from=build /out/linearcast-encoder /usr/local/bin/linearcast-encoder
COPY --from=build /out/linearcast-maint /usr/local/bin/linearcast-maint
COPY --from=build /out/encoder-dist /opt/linearcast/encoder-dist
COPY --from=web-build /src/web-ui/dist /usr/share/nginx/html
COPY deploy/nginx.single.conf.template /etc/nginx/nginx.conf.template
COPY deploy/linearcast-entrypoint.sh /usr/local/bin/linearcast-entrypoint
RUN chmod +x /usr/local/bin/linearcast-entrypoint

WORKDIR /app

ENV LINEARCAST_ADDR=:8888 \
    LINEARCAST_ADMIN_ADDR=:8890 \
    LINEARCAST_UPSTREAM_URL=http://127.0.0.1:8888 \
    LINEARCAST_ENCODER_DIST_DIR=/opt/linearcast/encoder-dist

EXPOSE 8080

ENTRYPOINT ["linearcast-entrypoint"]
CMD ["serve"]
