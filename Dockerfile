# syntax=docker/dockerfile:1
#
# orgscale server image. Releases are built by goreleaser (ko); this file is
# for building the image yourself:
#
#   docker build -t orgscale .
#
# The image holds no configuration. Pass settings as ORGSCALE_* environment
# variables or mount a config.yaml at /etc/orgscale/config.yaml, and keep
# /var/lib/orgscale (database and server key) on a volume.

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/orgscale ./cmd/orgscale \
 && mkdir -p /out/state /out/etc /out/run

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orgscale /usr/local/bin/orgscale
COPY --from=build --chown=65532:65532 /out/state /var/lib/orgscale
COPY --from=build --chown=65532:65532 /out/etc /etc/orgscale
COPY --from=build --chown=65532:65532 /out/run /var/run/orgscale
USER 65532:65532
VOLUME ["/var/lib/orgscale"]
EXPOSE 8080/tcp 3478/udp
ENTRYPOINT ["/usr/local/bin/orgscale"]
CMD ["serve"]
