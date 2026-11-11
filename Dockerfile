# syntax=docker/dockerfile:1

# Base images are pinned by digest so that a rebuild is reproducible and
# Dependabot proposes base updates as pull requests (which become releases).

# ---- web UI ----
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:a34ab6d7f6f4813f4d0c5469d74685604b962fbdbf5793a7bc7b4998ecb867c8 AS web
WORKDIR /src
COPY web/package.json web/package-lock.json web/
RUN cd web && npm ci
COPY web/ web/
RUN cd web && npm run build
COPY scripts/third-party-notices.sh scripts/
RUN sh scripts/third-party-notices.sh npm > /notices-npm.md

# ---- server (static, CGO-free binary; cross-compiled, no emulation) ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hoard ./cmd/hoard \
 && mkdir /out/data
# Licence texts of everything compiled or bundled into the image.
COPY scripts/third-party-notices.sh scripts/
COPY --from=web /notices-npm.md /notices-npm.md
RUN sh scripts/third-party-notices.sh go > /out/THIRD_PARTY_NOTICES.md \
 && cat /notices-npm.md >> /out/THIRD_PARTY_NOTICES.md

# ---- runtime: no shell, no package manager, non-root ----
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.title="hoard" \
      org.opencontainers.image.description="Self-hosted game-asset database with web UI, REST API and MCP server" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/hoard /hoard
COPY LICENSE /LICENSE
COPY --from=build /out/THIRD_PARTY_NOTICES.md /THIRD_PARTY_NOTICES.md
# 568 is the "apps" user TrueNAS SCALE runs apps as; owning /data makes named
# volumes and freshly created datasets writable without extra steps.
COPY --from=build --chown=568:568 /out/data /data
USER 568:568
ENV HOARD_ADDR=:8080 \
    HOARD_DATA_DIR=/data
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/hoard", "healthcheck"]
ENTRYPOINT ["/hoard"]
