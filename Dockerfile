# Base images pinned by digest; Dependabot proposes updates.

# Litestream streams every database change to the bucket (docs/PLAN.md §6). It is built here from
# its release source with our Go and the upgraded libraries in third_party/litestream (make
# litestream-deps): its release binary trails on both and fails the Trivy gate. Same build as
# upstream's: no cgo. Keep LITESTREAM_VERSION equal to the Makefile's.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS litestream
ARG TARGETOS TARGETARCH
ARG LITESTREAM_VERSION=v0.5.17
WORKDIR /src
RUN cp -R "$(go mod download -json github.com/benbjohnson/litestream@${LITESTREAM_VERSION} | sed -n 's/^.*"Dir": "\(.*\)",$/\1/p')"/. . \
 && chmod -R u+w .
COPY third_party/litestream/go.mod third_party/litestream/go.sum ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X main.Version=${LITESTREAM_VERSION#v}" -o /out/litestream ./cmd/litestream

# Go cross-compiles on the build machine's own platform: building the x86-64 release on an
# Apple Silicon Mac needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/saf ./cmd/saf \
 && mkdir /out/data

# distroless: no shell, no package manager; runs as uid 65532.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/saf /saf
COPY --from=litestream /out/litestream /litestream
COPY deploy/litestream.yml /etc/litestream.yml
# Owned by the nonroot user so a fresh Docker volume mounted here is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV SAF_DATA_DIR=/data
# Mount the key here read-only, from outside the data volume (docs/PLAN.md §1).
ENV SAF_KEY_FILE=/etc/saf/key
# 8080: plain HTTP (local) or Let's Encrypt + redirect; 8443: HTTPS when SAF_DOMAIN is set.
EXPOSE 8080 8443
# Litestream starts SAF itself (exec in litestream.yml). Needs SAF_REPLICA_URL.
ENTRYPOINT ["/litestream", "replicate", "-config", "/etc/litestream.yml"]
