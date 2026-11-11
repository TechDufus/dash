# syntax=docker/dockerfile:1

# The build stages run on the build machine's platform and Go cross-compiles for the target, so a
# multi-platform build needs no emulation: the final stage only copies files.
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
# No dependency's install script runs in the build (design H.2).
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/dash ./cmd/dash
# The data directory (dataDir's default, /data), copied into the image owned by the nonroot user.
# No VOLUME: an anonymous volume would make `docker run --read-only` writable and hide the data;
# mount -v dash-data:/data. No ENV DASH_DATA_DIR either: the environment outranks the config
# file, so it would override a file's dataDir.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG VERSION=dev
ARG REVISION=
ARG CREATED=
LABEL org.opencontainers.image.title="dash" \
      org.opencontainers.image.description="A dashboard for a homelab: Kubernetes, GitOps, Proxmox, alerts and metrics on one screen" \
      org.opencontainers.image.source="https://github.com/TechDufus/dash" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=go /out/dash /dash
COPY --from=go --chown=65532:65532 /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/dash/
USER 65532:65532
EXPOSE 8080 8081
# /readyz answers 503 for up to server.readyTimeout (60 s) while server.readyRequires plugins
# start, so the start period covers that plus one interval; a success during it is healthy at once.
HEALTHCHECK --interval=10s --timeout=3s --start-period=75s --retries=3 CMD ["/dash","healthcheck"]
ENTRYPOINT ["/dash"]
